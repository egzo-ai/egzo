package control

import (
	"context"
	"errors"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/egzo-ai/egzo/internal/version"
)

// mcpHandler serves the agent verbs as MCP tools over streamable HTTP, at /mcp on the agent port.
// Each request is bound to the authenticated agent, so a tool call can only act as that agent.
func (a *agentAPI) mcpHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		agent, _, _ := r.BasicAuth() // already verified by authenticatedHandler
		return a.mcpServer(agent)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

type textArgs struct {
	Text string `json:"text" jsonschema:"the text to send"`
}

type idArgs struct {
	ID string `json:"id" jsonschema:"the question id returned by ask_user"`
}

type handoffArgs struct {
	To   string `json:"to" jsonschema:"the name of the agent to hand the work to"`
	Text string `json:"text" jsonschema:"what you want it to do, with everything it needs to know"`
}

type noArgs struct{}

type okResult struct {
	OK bool `json:"ok"`
}

type askResult struct {
	ID string `json:"id" jsonschema:"keep this id and call get_answer with it later"`
}

type inboxResult struct {
	Messages []Message `json:"messages"`
}

func (a *agentAPI) mcpServer(agent string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "egzo", Version: version.Version}, &mcp.ServerOptions{
		Instructions: "Tools to talk to the people running this project and to report what you are doing. " +
			"Messages the humans queue for you arrive through check_inbox.",
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "status",
		Description: "Report in one short line what you are working on right now. It is shown next to your name in the project's overview.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in textArgs) (*mcp.CallToolResult, okResult, error) {
		if !validText(in.Text) {
			return nil, okResult{}, errors.New("text must not be empty or longer than 16 KB")
		}
		return nil, okResult{OK: true}, a.server.reportStatus(agent, in.Text)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "say",
		Description: "Tell the humans following this project something worth knowing, such as a result or a decision. Use it sparingly.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in textArgs) (*mcp.CallToolResult, okResult, error) {
		if !validText(in.Text) {
			return nil, okResult{}, errors.New("text must not be empty or longer than 16 KB")
		}
		return nil, okResult{OK: true}, a.server.say(agent, in.Text)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ask_user",
		Description: "Ask the humans a question you cannot answer yourself. It returns an id straight away; carry on with other work and call get_answer with the id later.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in textArgs) (*mcp.CallToolResult, askResult, error) {
		if !validText(in.Text) {
			return nil, askResult{}, errors.New("text must not be empty or longer than 16 KB")
		}
		id, err := a.server.ask(agent, in.Text)
		return nil, askResult{ID: id}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_answer",
		Description: "Check whether a question you asked with ask_user has been answered.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in idArgs) (*mcp.CallToolResult, Question, error) {
		question, ok := a.server.questionFor(agent, in.ID)
		if !ok {
			return nil, Question{}, errors.New("no such question")
		}
		return nil, question, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "check_inbox",
		Description: "Fetch the messages the humans or other agents queued for you. Each message is returned once.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, inboxResult, error) {
		messages, err := a.server.takeInbox(agent)
		return nil, inboxResult{Messages: messages}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "handoff",
		Description: "Hand work to another agent of this project: it is queued for that agent and typed into its terminal when it is idle. Say what you need and where to find it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in handoffArgs) (*mcp.CallToolResult, okResult, error) {
		if err := a.server.handoff(agent, in.To, in.Text); err != nil {
			return nil, okResult{}, err
		}
		return nil, okResult{OK: true}, nil
	})
	return server
}
