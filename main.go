package main

import (
	"log"

	"revda/internal/app"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}

	application, err := app.New()
	if err != nil {
		log.Fatalf("failed to initialize application: %v", err)
	}

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     "Revda",
			Width:     1100,
			Height:    700,
			MinWidth:  800,
			MinHeight: 500,
			Content:   ui.View(application.View),
		})
		application.SetWindow(win)
		application.Init()
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
