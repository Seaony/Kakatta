package main

import "github.com/Seaony/Kakatta/internal/devfixture"

func formalFixtureModelReply(schema, user string) (string, map[string]any, any, error) {
	return devfixture.ModelReply(schema, user)
}
