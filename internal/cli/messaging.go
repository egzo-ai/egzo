package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/egzo-ai/egzo/internal/stack"
)

// operatorActor is who the CLI acts as. The hub acts as the signed-in user instead.
const operatorActor = "operator"

func newSendCommand(opts *options) *cobra.Command {
	var interrupt bool
	cmd := &cobra.Command{
		Use:   "send AGENT MESSAGE...",
		Short: "Queue a message for an agent",
		Long: "Queue a message for an agent. The queue lives in the control sidecar, so every client, the CLI,\n" +
			"the hub and chat integrations, sends through the same path. The session holder types it into the\n" +
			"agent's terminal once the agent is idle and nobody has typed for a while (see `inject` in egzo.yaml).",
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
				return fmt.Errorf("no agent %q in egzo.yaml", args[0])
			}
			body, _ := json.Marshal(map[string]any{"to": args[0], "from": operatorActor, "text": strings.Join(args[1:], " "), "interrupt": interrupt})
			reply, err := stack.ControlRequest(ctx, s.engine, s.Resolved.Name, "POST", "/queue", body)
			if err != nil {
				return err
			}
			var queued struct{ ID string }
			json.Unmarshal(reply, &queued)
			fmt.Fprintf(cmd.OutOrStdout(), "queued %s for %s\n", queued.ID, args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&interrupt, "interrupt", false, "stop what the agent is doing first, so the message is typed in right away")
	return cmd
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

func newQuestionsCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "questions",
		Short: "List the questions agents have asked",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			reply, err := stack.ControlRequest(ctx, s.engine, s.Resolved.Name, "GET", "/questions", nil)
			if err != nil {
				return err
			}
			var questions []struct {
				ID, Agent, Text, Answer string
				Answered                bool
			}
			if err := json.Unmarshal(reply, &questions); err != nil {
				return err
			}
			table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "ID\tAGENT\tSTATE\tQUESTION")
			for _, q := range questions {
				state := "open"
				if q.Answered {
					state = "answered"
				}
				fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", q.ID, q.Agent, state, q.Text)
			}
			return table.Flush()
		},
	}
}

func newAnswerCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "answer ID ANSWER...",
		Short: "Answer a question an agent asked",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := commandContext(cmd)
			defer stop()
			s, err := openSession(ctx, opts)
			if err != nil {
				return err
			}
			defer s.close()
			body, _ := json.Marshal(map[string]string{"actor": operatorActor, "text": strings.Join(args[1:], " ")})
			if _, err := stack.ControlRequest(ctx, s.engine, s.Resolved.Name, "POST", "/questions/"+url.PathEscape(args[0])+"/answer", body); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "answered %s\n", args[0])
			return nil
		},
	}
}
