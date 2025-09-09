
# PDF Linearizer

A simple tool web based app for linearize pdf. This project using QPDF for linearize pdf file, and it not support for windows. If you want to run this project in windows, please use WSL instead.
## Features

- Linearize multiple pdf files

## Tech Stack

- QPDF
- Golang

## Run Locally

As I said before, This project using QPDF for linearize pdf file, and it not support for windows. If you want to run this project in windows, please use WSL instead.

Clone the project

```bash
  git clone https://github.com/amuhajirs/pdf-linearizer
```

Go to the project directory

```bash
  cd pdf-linearizer
```

Install dependencies

```bash
  go mod download
```

Install qpdf

```bash
sudo apt update
```

```bash
sudo apt install qpdf -y
```

Build app

```bash
  go build -o main
```

Start the server

```bash
  ./main
```


## Authors

- [@amuhajirs](https://www.github.com/amuhajirs)
