package main

import (
	"log"
	"os"

	"github.com/maxbolgarin/contem"
)

func main() {
	ctx := contem.New()
	defer ctx.Shutdown()

	file, err := os.OpenFile("out.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Println(err)
		return
	}
	ctx.AddFile(file)

	if _, err := file.Write([]byte("hello world\n")); err != nil {
		log.Println(err)
		return
	}

	if err := ctx.Shutdown(); err != nil {
		log.Println(err)
		return
	}

	if err := os.Remove("out.txt"); err != nil {
		log.Println(err)
		return
	}
}
