// Markdown as the Rich UI shows what an agent writes: CommonMark and GitHub's tables and strikethrough, never raw HTML. What an agent
// writes is not trusted: a model can be talked into writing anything, and a page of the hub runs with its viewer's session. markdown-it with
// html off escapes every tag, and its link check refuses javascript:, vbscript:, file: and data: (but images) addresses.

import MarkdownIt from "markdown-it";

const md = new MarkdownIt({ html: false, linkify: true, breaks: false, typographer: false });

// a link opens in a new tab, and the page it leads to learns nothing of the hub (no opener, no referrer)
const defaultLinkOpen = md.renderer.rules.link_open ?? ((tokens, i, options, _env, self) => self.renderToken(tokens, i, options));
md.renderer.rules.link_open = (tokens, i, options, env, self) => {
  tokens[i].attrSet("target", "_blank");
  tokens[i].attrSet("rel", "noopener noreferrer nofollow");
  return defaultLinkOpen(tokens, i, options, env, self);
};

// an image an agent names is not fetched: it would tell whoever serves it that someone reads the chat, and when. It shows as a link.
md.renderer.rules.image = (tokens, i) => {
  const token = tokens[i];
  const src = token.attrGet("src") ?? "";
  const alt = token.children?.map((c) => c.content).join("") || src;
  return `<a href="${md.utils.escapeHtml(src)}" target="_blank" rel="noopener noreferrer nofollow">${md.utils.escapeHtml(alt)}</a>`;
};

// a table scrolls on its own on a narrow screen instead of widening the chat
md.renderer.rules.table_open = () => '<div class="md-table"><table>\n';
md.renderer.rules.table_close = () => "</table></div>\n";

/** The HTML of what an agent wrote in Markdown: safe to put in the page as it is. */
export function renderMarkdown(source: string): string {
  return md.render(source);
}

/** The HTML of one line of Markdown (a tool's description, a title): no paragraph around it. */
export function renderInline(source: string): string {
  return md.renderInline(source);
}
