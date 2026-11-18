// Helpers for the HTML bodies produced by the WYSIWYG editor. Composed mail is
// sent as multipart/alternative, so an HTML body also needs a plain-text
// version for clients that don't render HTML.

const BLOCK_SELECTOR = 'p, div, li, tr, h1, h2, h3, h4, h5, h6, blockquote';

export function htmlToText(html: string): string {
  const doc = new DOMParser().parseFromString(html, 'text/html');

  doc.querySelectorAll('br').forEach((br) => br.replaceWith('\n'));
  doc.querySelectorAll(BLOCK_SELECTOR).forEach((block) => {
    block.insertAdjacentText('afterend', '\n');
  });

  return (doc.body.textContent || '')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}

export function isHtmlEmpty(html: string): boolean {
  return htmlToText(html).length === 0;
}
