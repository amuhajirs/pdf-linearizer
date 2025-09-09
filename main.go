package main

import (
	"archive/zip"
	"bytes"
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

func linearizeToWriter(f *multipart.FileHeader, writer io.Writer) error {
    // Simpan upload ke file sementara (sebagai input qpdf)
    tempInput, err := os.CreateTemp("", "input-*.pdf")
    if err != nil {
        return fmt.Errorf("temp input: %w", err)
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

    // qpdf input= temp file, output= stdout
    cmd := exec.Command("qpdf", "--linearize", tempInput.Name(), "-")

    stdout, err := cmd.StdoutPipe()
    if err != nil {
        return fmt.Errorf("stdout pipe: %w", err)
    }
    stderr, err := cmd.StderrPipe()
    if err != nil {
        return fmt.Errorf("stderr pipe: %w", err)
    }

    if err := cmd.Start(); err != nil {
        return fmt.Errorf("qpdf start: %w", err)
    }

    // capture stderr
    var stderrBuf bytes.Buffer
    go io.Copy(&stderrBuf, stderr)

    // stream hasil langsung ke writer (bisa ke response atau zip entry)
    if _, err := io.Copy(writer, stdout); err != nil {
        return fmt.Errorf("stream copy error: %w", err)
    }

    if err := cmd.Wait(); err != nil {
        return fmt.Errorf("qpdf failed: %w\nStderr:\n%s", err, stderrBuf.String())
    }

    if stderrBuf.Len() > 0 {
        log.Printf("qpdf warnings: %s", stderrBuf.String())
    }

    return nil
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
			c.String(http.StatusBadRequest, "Gagal membaca form")
			return
		}

		files := form.File["files"]
		if len(files) == 0 {
			c.String(http.StatusBadRequest, "Tidak ada file yang diunggah")
			return
		}

		// === CASE: SINGLE FILE ===
		if len(files) == 1 {
			file := files[0]
			c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", file.Filename))
			c.Header("Content-Type", "application/pdf")

			if err := linearizeToWriter(file, c.Writer); err != nil {
				fmt.Println("ERROR linearize:", err)
				c.String(http.StatusInternalServerError, "Linearize gagal: %v", err)
			}
			return
		}

		// === CASE: MULTI FILE (ZIP) ===
		c.Header("Content-Disposition", "attachment; filename=linearized_files.zip")
		c.Header("Content-Type", "application/zip")

		zipWriter := zip.NewWriter(c.Writer)
		var wg sync.WaitGroup
		var mu sync.Mutex

		// Worker pool limiter (misal 2 file paralel sekaligus)
		sem := make(chan struct{}, 2)

		for _, file := range files {
			wg.Add(1)
			go func(f *multipart.FileHeader) {
				defer wg.Done()
				sem <- struct{}{} // masuk pool
				defer func() { <-sem }()

				// Buat entry zip
				mu.Lock()
				entry, _ := zipWriter.Create(f.Filename)
				mu.Unlock()

				// Linearize langsung ke entry
				if err := linearizeToWriter(f, entry); err != nil {
					fmt.Printf("Linearize %s gagal: %v\n", f.Filename, err)
				} else {
					fmt.Printf("Linearize %s selesai\n", f.Filename)
				}
			}(file)
		}

		wg.Wait()
		zipWriter.Close()
	})

	r.Run(":8080")
}
