// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package hub

import (
	"context"
	"errors"
	"net"

	"github.com/egzo-ai/egzo/hub/internal/links"
	"github.com/egzo-ai/egzo/hub/internal/rbac"
	"github.com/egzo-ai/egzo/hub/internal/store"
)

// Workspaces lists a project's workspace volumes, for those who may read its files.
func (h *Hub) Workspaces(ctx context.Context, user store.User, id string) ([]links.Workspace, error) {
	if err := h.require(ctx, user, id, rbac.Files, "files.list"); err != nil {
		return nil, err
	}
	var list []links.Workspace
	ctx, cancel := context.WithTimeout(ctx, readLimit)
	defer cancel()
	err := h.call(ctx, id, func(ctx context.Context, e links.Engine) (err error) {
		list, err = e.Workspaces(ctx)
		return err
	})
	if errors.As(err, new(links.ErrNoProject)) {
		return nil, ErrNotFound
	}
	return list, err
}

// CanReadFiles checks that user may read the files of a project, and writes a refusal to its audit log.
func (h *Hub) CanReadFiles(ctx context.Context, user store.User, id string) error {
	return h.require(ctx, user, id, rbac.Files, "files.read")
}

// DialFiles connects to the file server of a workspace volume of a project. It lasts until it is closed or the link goes. The caller has
// checked CanReadFiles for the request it serves: a connection is kept and reused for the requests of others who may.
func (h *Hub) DialFiles(ctx context.Context, id, workspace, instance string) (net.Conn, error) {
	c := h.connected(id)
	if c == nil || !c.served.Load() {
		return nil, h.offline(ctx, id)
	}
	conn, err := c.link.DialFiles(ctx, workspace, instance)
	if err != nil {
		if c.gone() {
			return nil, h.offline(ctx, id)
		}
		return nil, err
	}
	ended := &endingConn{Conn: conn}
	ended.stop = context.AfterFunc(c.ctx, func() { conn.Close() })
	return ended, nil
}

// FileViewed writes to the project's audit log that a file was shown in the hub: what was read, and by whom.
func (h *Hub) FileViewed(ctx context.Context, user store.User, id, workspace, instance, path string) {
	where := workspace
	if instance != "" {
		where += " (worktree of " + instance + ")"
	}
	h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "files.view", Project: id, Agent: instance, Detail: where + ": " + path, Allowed: true})
}

// FileDownloaded writes a download to the project's audit log: what was read, and by whom.
func (h *Hub) FileDownloaded(ctx context.Context, user store.User, id, workspace, instance, path string) {
	where := workspace
	if instance != "" {
		where += " (worktree of " + instance + ")"
	}
	h.record(ctx, store.AuditEntry{Actor: rbac.Actor(user.ID), Action: "files.download", Project: id, Agent: instance, Detail: where + ": " + path, Allowed: true})
}
