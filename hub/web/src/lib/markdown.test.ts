import { describe, expect, it } from "vitest";
import { renderInline, renderMarkdown } from "./markdown";

describe("what an agent writes in Markdown", () => {
  it("is rendered: headings, lists, code, tables", () => {
    const html = renderMarkdown("# Plan\n\n- **one**\n- `two`\n\n```go\nfmt.Println(1)\n```\n\n| a | b |\n|---|---|\n| 1 | 2 |\n");
    expect(html).toContain("<h1>Plan</h1>");
    expect(html).toContain("<strong>one</strong>");
    expect(html).toContain("<code>two</code>");
    expect(html).toContain('<code class="language-go">fmt.Println(1)');
    expect(html).toContain('<div class="md-table"><table>');
  });

  it("never carries raw HTML", () => {
    const html = renderMarkdown('<img src=x onerror="alert(1)">\n\n<script>alert(1)</script>\n\nhi <b onclick="x()">there</b>');
    expect(html).not.toMatch(/<img|<script|<b /);
    expect(html).toContain("&lt;script&gt;");
  });

  it("refuses links that run code, and opens the others in a new tab without telling them where from", () => {
    const html = renderMarkdown("[a](javascript:alert(1)) [b](vbscript:x) [c](data:text/html,<script>x</script>) [d](https://example.com)");
    expect(html).not.toMatch(/href="(javascript|vbscript|data):/);
    expect(html).toContain('<a href="https://example.com" target="_blank" rel="noopener noreferrer nofollow">d</a>');
  });

  it("does not fetch the images it names: they are links", () => {
    const html = renderMarkdown('![a pixel](https://tracker.example/p.png "t")');
    expect(html).not.toContain("<img");
    expect(html).toContain('<a href="https://tracker.example/p.png" target="_blank" rel="noopener noreferrer nofollow">a pixel</a>');
  });

  it("makes bare addresses links", () => {
    expect(renderMarkdown("see https://example.com/x")).toContain('href="https://example.com/x"');
  });

  it("has an inline form, without a paragraph", () => {
    expect(renderInline("Run `ls -la`")).toBe("Run <code>ls -la</code>");
    expect(renderInline("<i>x</i>")).toBe("&lt;i&gt;x&lt;/i&gt;");
  });
});
