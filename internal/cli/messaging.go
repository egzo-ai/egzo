package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/stack"
)

// operatorActor is who the CLI acts as. The hub acts as the signed-in user instead.
const operatorActor = "operator"

func newSendCommand(opts *options) *cobra.Command {
	var interrupt, wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "send AGENT MESSAGE...",
		Short: "Send a request to an agent",
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
			if _, ok := s.Resolved.Agents[args[0]]; !ok {
				return fmt.Errorf("no agent %q in egzo.yaml (agents: %s)", args[0], strings.Join(agentNames(s.Resolved.Agents), ", "))
			}
			body, _ := json.Marshal(map[string]any{"to": "agent:" + args[0], "from": operatorActor, "text": strings.Join(args[1:], " "), "interrupt": interrupt})
			reply, err := stack.ControlRequest(ctx, s.engine, s.Resolved.Name, "POST", "/messages", body)
			if err != nil {
				return err
			}
			var queued struct {
				ID  string
				Seq int
			}
			json.Unmarshal(reply, &queued)
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
			fmt.Fprintf(cmd.ErrOrStderr(), "update: %s\n", event.Text)
		case event.Type == "resolved" && event.ID == id:
			outcome = event.Data.Outcome
			fmt.Fprintln(cmd.OutOrStdout(), event.Text)
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
		fmt.Fprintf(cmd.ErrOrStderr(), "no resolution after %s: message %s stays open (see `egzo messages`)\n", timeout, id)
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
		text := strings.Join(strings.Fields(m.Text), " ")
		if len(text) > 60 {
			text = text[:57] + "..."
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", m.ID, m.From, m.To, m.Kind, m.State, text)
	}
	return table.Flush()
}

func listMessages(cmd *cobra.Command, opts *options, query url.Values) error {
	ctx, stop := commandContext(cmd)
	defer stop()
	s, err := openSession(ctx, opts)
	if err != nil {
		return err
	}
	defer s.close()
	reply, err := stack.ControlRequest(ctx, s.engine, s.Resolved.Name, "GET", "/messages?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	var rows []messageRow
	if err := json.Unmarshal(reply, &rows); err != nil {
		return err
	}
	return writeMessages(cmd, rows)
}

func newMessagesCommand(opts *options) *cobra.Command {
	var agent string
	var all bool
	cmd := &cobra.Command{
		Use:   "messages",
		Short: "List the open messages",
		Long: "List the requests and questions that are not resolved yet, with where each stands (queued, announced, fetched,\n" +
			"unconfirmed). --all adds the resolved ones and the replies and updates; --agent narrows to one agent's messages.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			query := url.Values{}
			if agent != "" {
				query.Set("agent", agent)
			}
			if all {
				query.Set("all", "1")
			}
			return listMessages(cmd, opts, query)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "only messages sent or received by this agent")
	cmd.Flags().BoolVar(&all, "all", false, "include resolved messages, replies and updates")
	return cmd
}

func newQuestionsCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "questions",
		Short: "List the questions agents have asked and nobody has answered",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listMessages(cmd, opts, url.Values{"kind": {"question"}})
		},
	}
}

func newAnswerCommand(opts *options) *cobra.Command {
	var outcome string
	cmd := &cobra.Command{
		Use:   "answer ID ANSWER...",
		Short: "Answer a question an agent asked, or close a request it made of you",
		Long: "Resolve a message addressed to a person (see `egzo messages`): an answer to a question goes back to the agent\n" +
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
