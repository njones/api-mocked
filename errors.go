package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func saveError(dir string, fileSuffix string, err error) {
	f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%d-%s.txt", time.Now().Unix(), fileSuffix)))
	if err != nil {
		panic(err)
	}
	defer f.Close()
	fmt.Fprintf(f, "%s -", time.Now().Format(time.RFC3339Nano))
	fmt.Fprintln(f, err)
}

func showError(dir string) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		err := filepath.Walk(dir,
			func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				fmt.Fprintln(pw, path)
				io.Copy(pw, f)
				fmt.Fprintln(pw, "***")
				return nil
			})
		if err != nil {
			fmt.Fprint(pw, err)
		}
		pw.Close()
	}()
	return pr
}
