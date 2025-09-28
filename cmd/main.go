package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	config "github.com/SyneHQ/dramtic.jobs"
	"github.com/SyneHQ/dramtic.jobs/keys"
	"github.com/SyneHQ/dramtic.jobs/prisma/db"
)

func main() {

	log.Println("Starting Dramatic Jobs")

	failOnError := os.Getenv("ENVIRONMENT") != "development"

	_, err := keys.NewInfisicalSecrets(failOnError)

	if err != nil {
		if failOnError {
			os.Exit(1)
		}
		log.Printf("Error loading infisical secrets: %v", err)
	}

	log.Println("Loading config")

	config, err := config.Load()
	if err != nil {
		panic(err)
	}

	client := db.NewClient(
		db.WithDatasourceURL(config.DatabaseURL),
	)
	if err := client.Prisma.Connect(); err != nil {
		panic(err)
	}
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		if err := client.Prisma.Disconnect(); err != nil {
			panic(fmt.Errorf("could not disconnect: %w", err))
		}
		// clean up your webserver here
		// e.g. httpServer.Shutdown(ctx)
		os.Exit(0)
	}()

}
