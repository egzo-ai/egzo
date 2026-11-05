<script lang="ts">
  import { api, filesURL } from "../lib/api";
  import { errorText } from "../lib/app.svelte";
  import { ago, stamp } from "../lib/format";
  import { filesPlace, filesHref, humanSize } from "../lib/files";
  import { nav } from "../lib/router.svelte";
  import type { Listing, WorkspaceRow } from "../lib/types";
  import FileViewer from "./FileViewer.svelte";
  import Icon from "./Icon.svelte";
  import Pill from "./Pill.svelte";

  // The files of the project's workspaces, read only: the workspaces, then a folder at a time. A file is a download.
  let { project }: { project: string } = $props();

  const place = $derived(filesPlace(nav.path));
  const at = (workspace = "", instance = "", dir = "", file = "") => filesHref(project, workspace, instance, dir, file);

  let workspaces = $state<WorkspaceRow[] | null>(null);
  let listing = $state<Listing | null>(null);
  let failure = $state("");
  let asked = 0; // an answer to an older question is dropped

  $effect(() => {
    const { workspace, instance, dir, file } = place;
    const n = ++asked;
    failure = "";
    if (file) return; // the viewer reads it
    if (!workspace) {
      api.workspaces(project).then((w) => { if (n === asked) workspaces = w; }).catch((e) => { if (n === asked) failure = errorText(e); });
      return;
    }
    listing = null;
    api.listing(project, workspace, instance, dir).then((l) => { if (n === asked) listing = l; }).catch((e) => { if (n === asked) failure = errorText(e); });
  });

  // the folders above this one, each a link
  const crumbs = $derived.by(() => {
    const parts = place.dir.split("/").filter(Boolean);
    return parts.map((name, i) => ({ name, href: at(place.workspace, place.instance, parts.slice(0, i + 1).join("/") + "/") }));
  });
  const parent = $derived(place.dir ? at(place.workspace, place.instance, place.dir.split("/").filter(Boolean).slice(0, -1).map((p) => p + "/").join("")) : at());
</script>

{#if !place.workspace}
  {#if failure}
    <p class="field-error" role="alert">{failure}</p>
  {:else if workspaces === null}
    <p class="muted">Loading…</p>
  {:else if workspaces.length === 0}
    <div class="empty"><h2>No workspace</h2><p>The project has no workspace volume yet: <code>egzo project up</code> makes them.</p></div>
  {:else}
    <div class="table-wrap">
      <table class="table" data-testid="workspaces">
        <thead><tr><th>Workspace</th><th>Kind</th><th>State</th><th>Made by</th></tr></thead>
        <tbody>
          {#each workspaces as ws (ws.name)}
            <tr data-workspace={ws.name}>
              {#if ws.kind === "directory"}
                <td><a class="name" href={at(ws.name)}><Icon name="folder" size="sm" /> {ws.name}</a></td>
                <td class="muted">directory</td><td></td><td></td>
              {:else}
                <td><span class="name"><Icon name="folder" size="sm" /> {ws.name}</span></td>
                <td class="muted">git</td>
                <td class="muted" colspan="2" data-remote>
                  {#if ws.remote}<span title={ws.remote}>{ws.remote.replace(/^https:\/\//, "")}</span> · {ws.branch || "default branch"}{/if}
                  {#if !ws.worktrees.length}{#if ws.remote}<br />{/if}no worktree: spawn an agent that works in it{/if}
                </td>
              {/if}
            </tr>
            {#each ws.worktrees as wt (wt.instance)}
              <tr data-worktree={wt.instance}>
                <td style="padding-left:var(--space-6)"><a class="name" href={at(ws.name, wt.instance)}>{wt.instance}</a></td>
                <td class="muted">worktree</td>
                <td><Pill kind={wt.state === "running" ? "running" : "stopped"} label={wt.state === "removed" ? "agent removed" : wt.state} /></td>
                <td>{wt.creator}</td>
              </tr>
            {/each}
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{:else}
  <nav class="crumbs" aria-label="Folder">
    <a href={at()}>Workspaces</a> /
    <a href={at(place.workspace, place.instance)}>{place.workspace}{#if place.instance} · {place.instance}{/if}</a>
    {#each crumbs as c (c.href)} / <a href={c.href}>{c.name}</a>{/each}
    {#if place.file} / <span>{place.file}</span>{/if}
  </nav>
  {#if place.file}
    <FileViewer {project} workspace={place.workspace} instance={place.instance} path={place.dir + place.file} />
  {:else if failure}
    <p class="field-error" role="alert">{failure}</p>
  {:else if listing === null}
    <p class="muted">Loading…</p>
  {:else}
    <div class="table-wrap">
      <table class="table" data-testid="files">
        <thead><tr><th>Name</th><th style="text-align:right">Size</th><th>Modified</th></tr></thead>
        <tbody>
          <tr><td><a class="name" href={parent}>..</a></td><td></td><td></td></tr>
          {#each listing.entries as e (e.name)}
            <tr data-entry={e.name} data-type={e.type}>
              {#if e.type === "dir"}
                <td><a class="name" href={at(place.workspace, place.instance, place.dir + e.name + "/")}><Icon name="folder" size="sm" /> {e.name}/</a></td>
                <td></td>
              {:else if e.type === "file"}
                <td>
                  <a class="name" href={at(place.workspace, place.instance, place.dir, e.name)}><Icon name="file" size="sm" /> {e.name}</a>
                  <a class="muted download" href={filesURL(project, place.workspace, place.instance, place.dir + e.name)} download={e.name} data-external aria-label="Download {e.name}">download</a>
                </td>
                <td style="text-align:right" title="{e.size ?? 0} bytes">{humanSize(e.size ?? 0)}</td>
              {:else}
                <td><span class="muted" title="a symbolic link: not followed">{e.name} <small>link</small></span></td>
                <td></td>
              {/if}
              <td class="muted" title={stamp(e.modified)}>{ago(e.modified)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if listing.truncated}<p class="muted">This folder holds more than the hub lists: {listing.entries.length} are shown.</p>{/if}
  {/if}
{/if}
