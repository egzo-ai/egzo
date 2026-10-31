<script lang="ts">
  import { onMount } from "svelte";
  import sprite from "./lib/icons.svg?raw";
  import { app, boot, projectName, signOut } from "./lib/app.svelte";
  import { keys, startKeyboard } from "./lib/keyboard.svelte";
  import { nav, navigate, startRouter } from "./lib/router.svelte";
  import { reset, restore, showPage, workspace } from "./lib/workspace.svelte";
  import ConfirmHost from "./components/ConfirmHost.svelte";
  import KeyRef from "./components/KeyRef.svelte";
  import LeaderHud from "./components/LeaderHud.svelte";
  import Mbar from "./components/Mbar.svelte";
  import Palette from "./components/Palette.svelte";
  import Sidebar from "./components/Sidebar.svelte";
  import TermView from "./components/TermView.svelte";
  import Toasts from "./components/Toasts.svelte";
  import AgentPage from "./pages/AgentPage.svelte";
  import ErrorPage from "./pages/ErrorPage.svelte";
  import Home from "./pages/Home.svelte";
  import Project from "./pages/Project.svelte";
  import Register from "./pages/Register.svelte";
  import Settings from "./pages/Settings.svelte";
  import SignIn from "./pages/SignIn.svelte";

  onMount(() => {
    startRouter();
    const stopKeys = startKeyboard();
    // anything that navigates shows the page; the terminals stay open and mounted
    const onNavigate = () => showPage();
    window.addEventListener("egzo:navigate", onNavigate);
    void boot().then(() => {
      if (app.user) restore();
    });
    return () => {
      stopKeys();
      window.removeEventListener("egzo:navigate", onNavigate);
    };
  });

  // a signed-in person who lands on the sign-in screen goes where they were going
  $effect(() => {
    if (app.ready && app.user && nav.route.name === "login") {
      const next = new URLSearchParams(nav.path.split("?")[1] ?? "").get("next") ?? "/";
      navigate(next.startsWith("/") && !next.startsWith("//") ? next : "/", true);
      restore();
    }
  });

  $effect(() => {
    const r = nav.route;
    document.title =
      (r.name === "project" ? projectName(r.project) + " · " : r.name === "agent" ? r.agent + " · " : r.name === "register" ? "Register a project · " : r.name === "settings" ? ({ users: "Users", links: "Links", account: "Account" })[r.tab] + " · settings · " : r.name === "login" ? "Sign in · " : "") + "egzo hub";
  });

  const routeKey = $derived.by(() => {
    const r = nav.route;
    return r.name === "project" ? `p:${r.project}` : r.name === "agent" ? `a:${r.project}/${r.agent}` : r.name;
  });

  async function leave() {
    await signOut();
    reset();
    try {
      sessionStorage.removeItem("egzo.terminals");
    } catch {
      /* ignore */
    }
  }
</script>

<div style="display:none" aria-hidden="true">{@html sprite}</div>

{#if nav.route.name === "login"}
  <SignIn />
{:else if app.ready && app.user}
  <div class="app" data-rail={workspace.rail ? "" : undefined} data-fullscreen={workspace.fullscreen && workspace.active ? "" : undefined} data-nav-open={workspace.navOpen ? "" : undefined}>
    <Sidebar onsignout={leave} />
    <main class="stage">
      <Mbar />
      <div class="viewport">
        <div class="hubview" hidden={workspace.active !== null}>
          {#if app.offline}
            <div class="page" style="padding-bottom:0"><div class="banner" role="alert"><span class="tag">✕ Offline</span><div class="body">The hub cannot be reached. This page updates when it answers again.</div></div></div>
          {/if}
          {#key routeKey}
            {@const r = nav.route}
            {#if r.name === "home"}<Home />
            {:else if r.name === "settings"}<Settings tab={r.tab} />
            {:else if r.name === "register"}<Register code={r.code} />
            {:else if r.name === "project"}<Project id={r.project} tab={r.tab} />
            {:else if r.name === "agent"}<AgentPage project={r.project} agent={r.agent} tab={r.tab} />
            {:else}<ErrorPage error={new Error("There is no such page.")} />{/if}
          {/key}
        </div>
        <TermView hidden={workspace.active === null} />
      </div>
    </main>
  </div>
{/if}

{#if app.palette}<Palette />{/if}
{#if app.keyref}<KeyRef />{/if}
{#if keys.armed}<LeaderHud />{/if}
<ConfirmHost />
<Toasts />
