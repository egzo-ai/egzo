import { beforeEach, describe, expect, it } from "vitest";
import {
  closeTerm, cycleLayout, fillPanes, focusOtherPane, goTerm, noteOutput, openTerm, reconnect, reset, setLayout, showPage, showTerm, stepTerm, termById, workspace,
} from "./workspace.svelte";

beforeEach(() => {
  sessionStorage.clear();
  reset();
});

describe("fillPanes", () => {
  it("keeps what is shown, drops what closed, and fills with the rest in order", () => {
    expect(fillPanes(["a", "b", "c"], ["b"], 2)).toEqual(["b", "a"]);
    expect(fillPanes(["a", "c"], ["b", "c"], 2)).toEqual(["c", "a"]);
    expect(fillPanes(["a"], [], 4)).toEqual(["a"]);
    expect(fillPanes(["a", "b", "c"], [], 1)).toEqual(["a"]);
  });
});

describe("the workspace", () => {
  it("opens a terminal once, shows it, and remembers it for a reload", () => {
    openTerm("web", "coder-1");
    openTerm("web", "coder-1");
    expect(workspace.terms).toHaveLength(1);
    expect(workspace.active).toBe("web/coder-1");
    expect(JSON.parse(sessionStorage.getItem("egzo.terminals")!)).toEqual([["web", "coder-1"]]);
  });

  it("keeps terminals open when a page is shown", () => {
    openTerm("web", "a");
    showPage();
    expect(workspace.active).toBeNull();
    expect(workspace.terms).toHaveLength(1);
  });

  it("closing the one in view shows another, and the last one returns to the page", () => {
    openTerm("web", "a");
    openTerm("web", "b");
    closeTerm("web/b");
    expect(workspace.active).toBe("web/a");
    closeTerm("web/a");
    expect(workspace.active).toBeNull();
    expect(workspace.panes).toEqual([]);
  });

  it("steps through the page and the terminals, and goes to a number", () => {
    openTerm("web", "a");
    openTerm("web", "b");
    showPage();
    stepTerm(1);
    expect(workspace.active).toBe("web/a");
    stepTerm(1);
    expect(workspace.active).toBe("web/b");
    stepTerm(1);
    expect(workspace.active).toBeNull();
    stepTerm(-1);
    expect(workspace.active).toBe("web/b");
    goTerm(1);
    expect(workspace.active).toBe("web/a");
    goTerm(9);
    expect(workspace.active).toBe("web/a");
  });

  it("splits into panes and moves focus between them", () => {
    openTerm("web", "a");
    openTerm("web", "b");
    setLayout(2);
    expect(workspace.panes).toEqual(["web/b", "web/a"]);
    cycleLayout();
    expect(workspace.layout).toBe(4);
    cycleLayout();
    expect(workspace.layout).toBe(1);
    expect(workspace.panes).toHaveLength(1);
    setLayout(2);
    const before = workspace.focusPane;
    focusOtherPane();
    expect(workspace.focusPane).not.toBe(before);
    expect(workspace.active).toBe(workspace.panes[workspace.focusPane]);
  });

  it("marks output of a terminal that is not in view as unread, until it is shown", () => {
    openTerm("web", "a");
    openTerm("web", "b");
    noteOutput("web/a");
    expect(termById("web/a")!.unread).toBe(true);
    noteOutput("web/b");
    expect(termById("web/b")!.unread).toBe(false);
    showTerm("web/a");
    expect(termById("web/a")!.unread).toBe(false);
    showPage();
    noteOutput("web/a");
    expect(termById("web/a")!.unread).toBe(true);
  });

  it("reconnects by bumping the epoch, optionally taking the terminal over", () => {
    openTerm("web", "a");
    reconnect("web/a", true);
    expect(termById("web/a")).toMatchObject({ epoch: 1, takeover: true });
  });

  it("refuses to open more terminals than the limit", () => {
    for (let i = 0; i < 12; i++) expect(openTerm("web", `a${i}`)).toBe(true);
    expect(openTerm("web", "one-too-many")).toBe(false);
    expect(openTerm("web", "a3")).toBe(true); // one already open is only brought forward
  });
});
