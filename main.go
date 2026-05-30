package main

import (
	"context"
	"flag"
	"log"

	"h3terraform/internal/provider"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

var version string = "dev"

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run in debug mode")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/h3llo-cloud/h3",
		Debug:   debug,
	}

	err := providerserver.Serve(context.Background(), provider.New(version), opts)

	if err != nil {
		log.Fatal(err.Error())
	}
}
