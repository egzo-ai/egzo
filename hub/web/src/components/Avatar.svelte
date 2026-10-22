<script lang="ts">
  import { initials } from "../lib/format";
  import type { User } from "../lib/types";

  // The provider's picture when it has one and it loads, fetched by the browser and never by the hub; the initials otherwise.
  let { user, me = false }: { user: Pick<User, "username" | "name" | "picture">; me?: boolean } = $props();
  let failed = $state(false);
  const label = $derived(initials(user.name || user.username));
</script>

{#if user.picture && !failed}
  <img class="avatar" data-me={me ? "" : undefined} src={user.picture} alt="" referrerpolicy="no-referrer" loading="lazy" onerror={() => (failed = true)} />
{:else}
  <span class="avatar" data-me={me ? "" : undefined}>{label}</span>
{/if}
