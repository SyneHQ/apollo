package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	config "github.com/SyneHQ/dramtic.jobs"
	"github.com/SyneHQ/dramtic.jobs/keys"
	"github.com/SyneHQ/dramtic.jobs/proto"
	"github.com/SyneHQ/dramtic.jobs/runner"
	jobsserver "github.com/SyneHQ/dramtic.jobs/server"
	"google.golang.org/grpc"
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

	// Choose runner
	var r runner.Runner
	switch config.JobsProvider {
	case "cloudrun":
		r = runner.NewCloudRunRunner(config.GCPProjectID, config.GCPRegion, config.Jobs.Image)
	default:
		r = runner.NewLocalRunner(config.Jobs.Image)
	}

	// Start gRPC server
	lis, err := net.Listen("tcp", ":"+config.Port)
	if err != nil {
		panic(err)
	}
	grpcServer := grpc.NewServer()
	js := jobsserver.NewJobsServer(r, config)
	js.Reload(context.Background())
	proto.RegisterJobsServiceServer(grpcServer, js)
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			panic(err)
		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		grpcServer.GracefulStop()
		// clean up your webserver here
		// e.g. httpServer.Shutdown(ctx)
		os.Exit(0)
	}()
}
