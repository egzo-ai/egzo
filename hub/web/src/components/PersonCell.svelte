<script lang="ts">
  import type { User } from "../lib/types";
  import Avatar from "./Avatar.svelte";

  // A person as every list shows them: the username the hub chose, then what their provider says, which tells two "Alex"es apart.
  let { user, me = false }: { user: User; me?: boolean } = $props();
</script>

<span class="presence"><Avatar {user} {me} /><b>{user.username}</b></span>
{#if user.name && user.name !== user.username}<span class="muted">{user.name}</span>{/if}
{#if user.email}<span class="muted" title={user.emailVerified ? "" : "The provider does not say this address is verified"}>{user.email}{user.emailVerified ? "" : " (unverified)"}</span>{/if}
{#if user.disabled}<span class="badge" data-testid="disabled-badge">disabled</span>{/if}
