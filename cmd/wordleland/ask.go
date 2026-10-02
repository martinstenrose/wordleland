package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/martinstenrose/wordleland/internal/config"
	"github.com/martinstenrose/wordleland/internal/i18n"
	"github.com/martinstenrose/wordleland/internal/reply"
)

// runAsk puts one question to the bot and prints what it would post, for
// trying a question out without asking it in the group. Everything is the
// bot's own — the database, the model, the answer — except that nothing
// is posted and nothing is kept.
func runAsk(e *env, args []string) error {
	fs := flagSet(e, "ask")
	player := fs.String("player", "", "ask as this `player`, so \"I\" and \"me\" mean them")
	after := fs.String("after", "", "ask this `question` first, so the one given can follow on from it")
	model := fs.String("model", envOrDefault("LLM_MODEL", config.DefaultLLMModel), "the `model` to ask")
	url := fs.String("url", envOrDefault("LLM_URL", config.DefaultLLMURL), "the model server's `URL`")
	locale := fs.String("locale", envOrDefault("SIGNAL_LOCALE", i18n.Default), "the `language` to answer in")
	wait := fs.Duration("wait", prepareFor, "how long to wait for the model to be pulled and loaded")
	if err := fs.Parse(args); err != nil {
		return err
	}
	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		return fmt.Errorf("no question given: wordleland ask [--player name] \"vem leder?\"")
	}
	cats, err := i18n.Load()
	if err != nil {
		return err
	}
	o, err := readyModel(e.ctx, e.out, *url, *model, *wait)
	if err != nil {
		return err
	}

	var previous *reply.Request
	if earlier := strings.TrimSpace(*after); earlier != "" {
		req, err := askOne(e, cats, *locale, o, *player, nil, earlier)
		if err != nil {
			return err
		}
		previous = &req
		fmt.Fprintln(e.out)
	}
	_, err = askOne(e, cats, *locale, o, *player, previous, question)
	return err
}

// askOne asks one question and prints how it was placed and the answer.
func askOne(e *env, cats i18n.Catalogues, locale string, interp reply.Interpreter, player string,
	previous *reply.Request, question string) (reply.Request, error) {

	start := time.Now()
	req, text, err := reply.Ask(e.ctx, e.db, cats, locale, interp, player, previous, question)
	if err != nil {
		return reply.Request{}, err
	}
	fmt.Fprintf(e.out, "%s\nplaced as: %s (%.1f s)\n\n%s\n", question, reply.Describe(req),
		time.Since(start).Seconds(), text)
	return req, nil
}
