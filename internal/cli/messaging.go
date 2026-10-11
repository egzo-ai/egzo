// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/stack"
	"github.com/egzo-ai/egzo/internal/termsafe"
)

// operatorActor is who the CLI acts as. The hub acts as the signed-in user instead.
const operatorActor = "operator"

func newSendCommand(opts *options) *cobra.Command {
	var interrupt, wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "send INSTANCE MESSAGE...",
		Short: "Send a request to an instance",
		Long: "Send a request to an agent, as the operator. The message waits in the control sidecar; when the agent is idle\n" +
			"and nobody has typed for a while, a short line is typed into its terminal naming the message, and the agent\n" +
			"fetches it and answers through its tools. --wait waits for that answer and prints it: updates go to stderr,\n" +
			"the result to stdout, and the exit code is 0 for done, 3 for declined, 4 for failed, 5 when --timeout passes\n" +
			"(the message stays open).",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			queued, err := queueMessage(ctx, s, args[0], strings.Join(args[1:], " "), interrupt)
			if err != nil {
				return err
			}
			line := fmt.Sprintf("queued %s for %s\n", queued.ID, args[0])
			if !wait {
				fmt.Fprint(cmd.OutOrStdout(), line)
				return nil
			}
			fmt.Fprint(cmd.ErrOrStderr(), line)
			return waitForResolution(ctx, cmd, s, args[0], queued.ID, queued.Seq, timeout)
		},
	}
	cmd.Flags().BoolVar(&interrupt, "interrupt", false, "stop what the agent is doing first, so the message is announced right away")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the agent to resolve the request and print its answer")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "with --wait, give up after this long (exit code 5); the message stays open")
	return cmd
}

// queued is a message waiting in the control sidecar.
type queued struct {
	ID  string
	Seq int
}

// queueMessage sends a request from the operator to an instance.
func queueMessage(ctx context.Context, s *session, agent, text string, interrupt bool) (queued, error) {
	if instance, isInstance := s.observed.Instance(agent); !isInstance || instance.State == "missing" {
		if _, isTemplate := s.Resolved.Agents[agent]; isTemplate {
			return queued{}, fmt.Errorf("%s is a template; spawn it first: egzo agent spawn %s", agent, agent)
		}
		return queued{}, fmt.Errorf("no instance %q in this project (instances: %s)", agent, strings.Join(s.observed.InstanceNames(), ", "))
	}
	body, _ := json.Marshal(map[string]any{"to": "agent:" + agent, "from": operatorActor, "text": text, "interrupt": interrupt})
	reply, err := stack.ControlRequest(ctx, s.engine, s.Resolved.Name, "POST", "/messages", body)
	if err != nil {
		return queued{}, err
	}
	var q queued
	json.Unmarshal(reply, &q)
	return q, nil
}

// Exit codes of `send --wait`.
const (
	exitDeclined = 3
	exitFailed   = 4
	exitTimedOut = 5
)

// waitForResolution follows the event stream from the message on: updates are printed to stderr as they
// arrive, and the resolution's text to stdout.
func waitForResolution(ctx context.Context, cmd *cobra.Command, s *session, agent, id string, seq int, timeout time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	streamCtx, stopStream := context.WithCancel(ctx)
	defer stopStream()

	outcome := ""
	watcher := &lineWatcher{handle: func(line []byte) {
		var event struct {
			Type string
			ID   string
			Text string
			Data struct {
				Kind    string
				Re      string
				Outcome string
			}
		}
		if json.Unmarshal(line, &event) != nil {
			return
		}
		switch {
		case event.Type == "message" && event.Data.Kind == "update" && event.Data.Re == id:
			fmt.Fprintf(cmd.ErrOrStderr(), "update: %s\n", termsafe.Block(event.Text))
		case event.Type == "resolved" && event.ID == id:
			outcome = event.Data.Outcome
			printUntrusted(cmd.OutOrStdout(), event.Text)
			stopStream()
		}
	}}
	query := url.Values{"agent": {agent}, "follow": {"1"}, "after": {fmt.Sprint(seq - 1)}}
	err := stack.ControlStream(streamCtx, s.engine, s.Resolved.Name, "GET", "/events?"+query.Encode(), watcher, cmd.ErrOrStderr())
	switch outcome {
	case "done":
		return nil
	case "declined":
		return ExitError{Code: exitDeclined}
	case "failed":
		return ExitError{Code: exitFailed}
	}
	if ctx.Err() != nil && timeout > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "no resolution after %s: message %s stays open (see `egzo message ls`)\n", timeout, id)
		return ExitError{Code: exitTimedOut}
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("the event stream ended before message %s was resolved", id)
}

// lineWatcher hands each complete line written to it to a function.
type lineWatcher struct {
	handle func([]byte)
	buffer []byte
}

func (w *lineWatcher) Write(p []byte) (int, error) {
	w.buffer = append(w.buffer, p...)
	for {
		end := bytes.IndexByte(w.buffer, '\n')
		if end < 0 {
			return len(p), nil
		}
		w.handle(w.buffer[:end])
		w.buffer = w.buffer[end+1:]
	}
}

func newEventsCommand(opts *options) *cobra.Command {
	var follow bool
	var agent string
	var after int
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show the typed event stream, one JSON event per line",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			query := url.Values{}
			if agent != "" {
				query.Set("agent", agent)
			}
			if after > 0 {
				query.Set("after", fmt.Sprint(after))
			}
			if follow {
				query.Set("follow", "1")
			}
			return stack.ControlStream(ctx, s.engine, s.Resolved.Name, "GET", "/events?"+query.Encode(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep the stream open for new events")
	cmd.Flags().StringVar(&agent, "agent", "", "only events of this agent")
	cmd.Flags().IntVar(&after, "after", 0, "only events after this sequence number")
	return cmd
}

// messageRow is a message as the operator API lists it.
type messageRow struct {
	ID, From, To, Kind, State, Text string
}

func writeMessages(cmd *cobra.Command, rows []messageRow) error {
	table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tFROM\tTO\tKIND\tSTATE\tTEXT")
	for _, m := range rows {
		// what the agent wrote is data, never something the terminal may act on
		text := termsafe.Truncate(termsafe.Line(m.Text), 60)
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", termsafe.Line(m.ID), termsafe.Line(m.From), termsafe.Line(m.To), termsafe.Line(m.Kind), termsafe.Line(m.State), text)
	}
	return table.Flush()
}

func fetchMessages(cmd *cobra.Command, opts *options, query url.Values) ([]messageRow, error) {
	ctx, stop := commandContext(cmd)
	defer stop()
	s, err := openSession(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer s.close()
	reply, err := stack.ControlRequest(ctx, s.engine, s.Resolved.Name, "GET", "/messages?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var rows []messageRow
	if err := json.Unmarshal(reply, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

var (
	messageKinds  = []string{"request", "question", "reply", "update"}
	messageStates = []string{"pending", "resolved"}
)

func oneOf(flag, value string, allowed []string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("%s %q is not one of %s", flag, value, strings.Join(allowed, ", "))
}

// sameParty says whether a message's sender or recipient (`operator`, `agent:coder-1`) is the one asked for,
// which may be given as the bare name of an agent.
func sameParty(party, want string) bool { return party == want || party == "agent:"+want }

func newMessageListCommand(opts *options) *cobra.Command {
	var kind, state, to, from string
	var all bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List messages",
		Long: "List the requests and questions that are not resolved yet, with where each stands (queued, announced, fetched,\n" +
			"unconfirmed). Filters narrow the list: --kind (request, question, reply or update), --state (pending: not\n" +
			"resolved yet; resolved), --to and --from (operator, or an agent's name). Asking for resolved messages, or\n" +
			"for replies and updates, looks through the whole record; --all lists it all.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if kind != "" {
				if err := oneOf("--kind", kind, messageKinds); err != nil {
					return err
				}
			}
			if state != "" {
				if err := oneOf("--state", state, messageStates); err != nil {
					return err
				}
			}
			query := url.Values{}
			if all || state == "resolved" || kind == "reply" || kind == "update" {
				query.Set("all", "1")
			}
			if kind != "" {
				// what the control API calls a reply is a resolution
				if kind == "reply" {
					query.Set("kind", "resolution")
				} else {
					query.Set("kind", kind)
				}
			}
			rows, err := fetchMessages(cmd, opts, query)
			if err != nil {
				return err
			}
			var shown []messageRow
			for _, m := range rows {
				switch {
				case state == "pending" && m.State == "resolved", state == "resolved" && m.State != "resolved":
				case to != "" && !sameParty(m.To, to), from != "" && !sameParty(m.From, from):
				default:
					shown = append(shown, m)
				}
			}
			return writeMessages(cmd, shown)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "only messages of this kind: "+strings.Join(messageKinds, ", "))
	cmd.Flags().StringVar(&state, "state", "", "only messages in this state: "+strings.Join(messageStates, ", "))
	cmd.Flags().StringVar(&to, "to", "", "only messages sent to this party (operator, or an agent's name)")
	cmd.Flags().StringVar(&from, "from", "", "only messages sent by this party (operator, or an agent's name)")
	cmd.Flags().BoolVar(&all, "all", false, "include resolved messages, replies and updates")
	return cmd
}

func newMessageReplyCommand(opts *options) *cobra.Command {
	var outcome string
	cmd := &cobra.Command{
		Use:   "reply ID ANSWER...",
		Short: "Answer a question an agent asked, or close a request it made of you",
		Long: "Resolve a message addressed to a person (see `egzo message ls`): an answer to a question goes back to the agent\n" +
			"as a reply, announced in its terminal like any message. A message addressed to an agent can only be resolved\n" +
			"by that agent.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			body, _ := json.Marshal(map[string]string{"actor": operatorActor, "text": strings.Join(args[1:], " "), "outcome": outcome})
			if _, err := stack.ControlRequest(ctx, s.engine, s.Resolved.Name, "POST", "/messages/"+url.PathEscape(args[0])+"/resolve", body); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "answered %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&outcome, "outcome", "done", "done, declined or failed")
	return cmd
}

func newMessageCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "message",
		Aliases: []string{"msg"},
		Short:   "Requests and questions between you and the agents",
	}
	cmd.AddCommand(newSendCommand(opts), newMessageListCommand(opts), newMessageReplyCommand(opts))
	return group(cmd)
}

// printUntrusted prints what an agent answered: its words, without any control sequence.
func printUntrusted(out io.Writer, text string) { fmt.Fprintln(out, termsafe.Block(text)) }
