//go:build fake

package main

import (
	"github.com/Ahhhh-man/registry-ui/internal/config"
	"github.com/Ahhhh-man/registry-ui/internal/registry"
	"github.com/Ahhhh-man/registry-ui/internal/registry/fake"
)

func init() {
	newSource = func(config.Config) (registry.Source, error) { return fake.New(), nil }
}
