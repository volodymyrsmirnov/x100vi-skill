// Package x100viskill embeds the files the binary ships: starter recipes, the recipe JSON schema and
// the agent skill (installed by `x100vi install-skill`).
package x100viskill

import "embed"

//go:embed assets/recipes/*.json assets/recipe.schema.json
var Assets embed.FS

//go:embed all:skills/x100vi
var Skill embed.FS
