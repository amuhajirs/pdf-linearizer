package main

import (
	"archive/zip"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"sync"

	"github.com/gin-gonic/gin"
)

func linearizeToWriter(f *multipart.FileHeader, w io.Writer) error {
	tempInput, err := os.CreateTemp("", "input-*.pdf")
	if err != nil {
		return fmt.Errorf("create temp input: %w", err)
	}
	defer os.Remove(tempInput.Name())
	defer tempInput.Close()

	src, err := f.Open()
	if err != nil {
		return fmt.Errorf("open upload: %w", err)
	}
	if _, err := io.Copy(tempInput, src); err != nil {
		return fmt.Errorf("copy upload: %w", err)
	}
	src.Close()

	cmd := exec.Command("qpdf", "--linearize", tempInput.Name(), "-")
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// linearizeToTempFile: run qpdf and save the result to a temp file
func linearizeToTempFile(f *multipart.FileHeader) (string, error) {
	// save input to temp file
	tempInput, err := os.CreateTemp("", "input-*.pdf")
	if err != nil {
		return "", fmt.Errorf("create temp input: %w", err)
	}
	defer os.Remove(tempInput.Name())
	defer tempInput.Close()

	src, err := f.Open()
	if err != nil {
		return "", fmt.Errorf("open upload: %w", err)
	}
	if _, err := io.Copy(tempInput, src); err != nil {
		return "", fmt.Errorf("copy upload: %w", err)
	}
	src.Close()

	// output temp
	tempOutput, err := os.CreateTemp("", "output-*.pdf")
	if err != nil {
		return "", fmt.Errorf("create temp output: %w", err)
	}
	tempOutput.Close()

	// run qpdf
	cmd := exec.Command("qpdf", "--linearize", tempInput.Name(), tempOutput.Name())
	out, err := cmd.CombinedOutput()

	// check if the resulting file actually exists & has content
	info, statErr := os.Stat(tempOutput.Name())
	if statErr != nil || info.Size() == 0 {
		os.Remove(tempOutput.Name())
		return "", fmt.Errorf("qpdf failed completely: %w, stderr: %s", err, string(out))
	}

	// if qpdf returns exit code != 0 but the file is created -> just a warning
	if err != nil {
		log.Printf("qpdf warning for %s: %s", f.Filename, string(out))
	}

	return tempOutput.Name(), nil
}

type job struct {
	file *multipart.FileHeader
}

type result struct {
	filename string
	tempPath string
	err      error
}

func main() {
	r := gin.Default()

	r.Static("/static", "./static")
	r.SetHTMLTemplate(template.Must(template.ParseFiles("templates/index.html")))

	// Form upload
	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", nil)
	})

	// Upload & linearize
	r.POST("/linearize", func(c *gin.Context) {
		form, err := c.MultipartForm()
		if err != nil {
			c.String(http.StatusBadRequest, "Failed to read form")
			return
		}

		files := form.File["files"]
		if len(files) == 0 {
			c.String(http.StatusBadRequest, "No files uploaded")
			return
		}

		fmt.Printf("Processing %d file(s)\n", len(files))

		// === CASE: SINGLE FILE ===
		if len(files) == 1 {
			file := files[0]
			c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", file.Filename))
			c.Header("Content-Type", "application/pdf")

			if err := linearizeToWriter(file, c.Writer); err != nil {
				log.Printf("ERROR linearize: %v", err)
				// Write plain text error (note: headers already set to pdf, but this is best-effort)
				c.String(http.StatusInternalServerError, "Linearize failed: %v", err)
				return
			}
			return
		}

		// === CASE: MULTI FILE (ZIP) ===
		c.Header("Content-Disposition", "attachment; filename=linearized_files.zip")
		c.Header("Content-Type", "application/zip")
		c.Header("Transfer-Encoding", "chunked")

		// Channel for jobs and results
		jobs := make(chan job, len(files))
		results := make(chan result, len(files))

		// Number of workers (adjustable)
		workerCount := 3
		var wg sync.WaitGroup

		// Start workers
		for w := 0; w < workerCount; w++ {
			wg.Add(1)
			fmt.Printf("Worker %d started\n", w)
			go func(workerID int) {
				defer wg.Done()
				fmt.Printf("Worker %d running\n", workerID)
				for j := range jobs {
					fmt.Printf("Worker %d: processing %s\n", workerID, j.file.Filename)
					tempPath, err := linearizeToTempFile(j.file)
					results <- result{
						filename: j.file.Filename,
						tempPath: tempPath,
						err:      err,
					}
					if err != nil {
						fmt.Printf("Worker %d: ERROR %s - %v\n", workerID, j.file.Filename, err)
					} else {
						fmt.Printf("Worker %d: SUCCESS %s -> %s\n", workerID, j.file.Filename, tempPath)
					}
				}
			}(w)
		}

		// Producer: send all jobs
		for _, f := range files {
			jobs <- job{file: f}
		}
		close(jobs)
		fmt.Printf("All jobs have been sent to the queue\n")

		// Closer goroutine: close results channel after all workers are done
		go func() {
			wg.Wait()
			close(results)
			fmt.Printf("All workers finished, results channel closed\n")
		}()

		// === SEQUENTIAL ZIP CREATION STAGE (streaming) ===
		zipWriter := zip.NewWriter(c.Writer)
		// Ensure zip central directory is written before returning
		defer func() {
			if err := zipWriter.Close(); err != nil {
				log.Printf("zipWriter.Close error: %v", err)
			}
		}()

		// Try to get flusher
		flusher, flushOK := c.Writer.(http.Flusher)

		// Slice to store temp files that need to be cleaned up
		var tempFilesToCleanup []string
		defer func() {
			// Cleanup all temp files
			for _, tempPath := range tempFilesToCleanup {
				if tempPath != "" {
					os.Remove(tempPath)
				}
			}
		}()

		successCount := 0
		totalFiles := len(files)

		// Process all results from workers as they arrive
		for res := range results {
			fmt.Println("Reading worker result:", res.filename)

			if res.err != nil {
				log.Printf("Linearize %s failed: %v", res.filename, res.err)
				continue
			}

			// Add to cleanup list
			tempFilesToCleanup = append(tempFilesToCleanup, res.tempPath)

			// Check if temp file exists and is not empty
			fileInfo, err := os.Stat(res.tempPath)
			if err != nil {
				log.Printf("Temp file %s not found: %v", res.tempPath, err)
				continue
			}
			if fileInfo.Size() == 0 {
				log.Printf("Temp file %s is empty", res.tempPath)
				continue
			}

			// Create entry in ZIP
			entry, err := zipWriter.Create(res.filename)
			if err != nil {
				log.Printf("Failed to create ZIP entry for %s: %v", res.filename, err)
				continue
			}

			// Read and copy temp file to ZIP entry
			tempFile, err := os.Open(res.tempPath)
			if err != nil {
				log.Printf("Failed to open temp file %s: %v", res.tempPath, err)
				continue
			}

			written, err := io.Copy(entry, tempFile)
			tempFile.Close()

			if err != nil {
				log.Printf("Failed to copy data to ZIP for %s: %v", res.filename, err)
				continue
			}

			// Flush the response so browser starts receiving bytes immediately
			if flushOK {
				flusher.Flush()
				fmt.Printf("Flushed after adding %s (%d bytes)\n", res.filename, written)
			} else {
				fmt.Printf("Added %s (%d bytes) - flusher not available\n", res.filename, written)
			}

			fmt.Printf("✓ %s successfully added to ZIP (%d bytes)\n", res.filename, written)
			successCount++
		}

		// After results loop ends, zipWriter.Close() will be called by deferred function above.
		fmt.Printf("ZIP creation finished: %d/%d files successful\n", successCount, totalFiles)
	})

	r.Run(":8080")
}
