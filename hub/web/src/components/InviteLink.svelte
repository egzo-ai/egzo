<script lang="ts">
  import { toast } from "../lib/app.svelte";
  import { copyText } from "../lib/clipboard";
  import { stamp } from "../lib/format";
  import type { NewInvite } from "../lib/types";

  let { invite }: { invite: NewInvite } = $props();

  async function copy() {
    toast((await copyText(invite.url)) ? "Copied" : "Copy it by hand: the browser refused", "ok");
  }
</script>

<section class="panel" data-testid="new-invite" style="max-width:80ch">
  <div class="pad" style="display:grid;gap:var(--space-3)">
    <p style="margin:0">This link is shown once. It is good once, until {stamp(invite.expiresAt)}, and for whoever opens it: nothing ties it to a person. Hand it over the way you would a password.</p>
    <dl class="kv"><dt>link</dt><dd><code data-testid="invite-link">{invite.url}</code></dd></dl>
    <div class="btn-group"><button class="btn" data-size="sm" onclick={copy}>Copy</button></div>
  </div>
</section>
