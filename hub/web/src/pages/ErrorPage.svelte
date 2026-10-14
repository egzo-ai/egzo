<script lang="ts">
  import { ApiError } from "../lib/api";
  import { href } from "../lib/router.svelte";

  let { error }: { error: unknown } = $props();
  const status = $derived(error instanceof ApiError ? error.status : 0);
  const title = $derived(status === 404 ? "Not found" : status === 403 ? "Forbidden" : status === 0 ? "The hub cannot be reached" : "Something went wrong");
  const message = $derived(error instanceof Error ? error.message : String(error));
</script>

<div class="page" data-narrow>
  <div class="empty" role="alert">
    <h2>{title}</h2>
    <p>{message}</p>
    <p><a href={href.home()}>Back to the projects</a></p>
  </div>
</div>
