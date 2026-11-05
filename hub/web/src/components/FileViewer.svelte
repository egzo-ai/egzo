<script lang="ts">
  import { api, filesURL } from "../lib/api";
  import { errorText } from "../lib/app.svelte";
  import { humanSize, viewLines } from "../lib/files";
  import type { FileView } from "../lib/types";
  import Icon from "./Icon.svelte";

  // One file of a workspace, shown as text when its bytes are text: never rendered, never run. What an agent wrote is put in the page as
  // text nodes only. A download is always there.
  let { project, workspace, instance, path }: { project: string; workspace: string; instance: string; path: string } = $props();

  let view = $state<FileView | null>(null);
  let failure = $state("");
  let asked = 0; // an answer to an older question is dropped

  $effect(() => {
    const n = ++asked;
    view = null;
    failure = "";
    api.view(project, workspace, instance, path).then((v) => { if (n === asked) view = v; }).catch((e) => { if (n === asked) failure = errorText(e); });
  });

  const name = $derived(path.split("/").pop() ?? path);
  const lines = $derived(view?.kind === "text" ? viewLines(view.text) : []);
</script>

<div class="viewer-head">
  <h2 class="viewer-name">{name}</h2>
  {#if view}<span class="muted">{humanSize(view.size)}</span>{/if}
  <a class="btn" data-size="sm" href={filesURL(project, workspace, instance, path)} download={name} data-external><Icon name="download" size="sm" /> Download</a>
</div>
{#if failure}
  <p class="field-error" role="alert">{failure}</p>
{:else if view === null}
  <p class="muted">Loading…</p>
{:else if view.kind === "binary"}
  <div class="empty" data-testid="not-text"><h2>Not shown</h2><p>This file is not text ({view.binary}). Download it to open it.</p></div>
{:else}
  {#if view.truncated || view.cutLines}
    <p class="muted viewer-note">
      {#if view.truncated}Only the first 256 KB of {humanSize(view.size)} are shown.{/if}
      {#if view.cutLines}{view.cutLines} {view.cutLines === 1 ? "line is" : "lines are"} cut at 10,000 characters.{/if}
    </p>
  {/if}
  {#if view.bidi}
    <p class="field-error" role="alert" data-testid="bidi">
      This file holds {view.bidi} {view.bidi === 1 ? "character" : "characters"} that change the direction of text, so it may read otherwise
      than it is. {view.bidi === 1 ? "It is" : "Each is"} marked where it stands.
    </p>
  {/if}
  {#if lines.length === 0}
    <p class="muted" data-testid="viewer">The file is empty.</p>
  {:else}
    <pre class="spec" data-testid="viewer">{#each lines as line, i (i)}<span class="l">{#each line as piece, j (j)}{#if "bidi" in piece}<mark class="bidi" title="A character that changes the direction of text">⟨{piece.bidi}⟩</mark>{:else}{piece.text}{/if}{/each}</span>{/each}</pre>
  {/if}
{/if}
