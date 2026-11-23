// Email HTML sanitization using DOMPurify.
// Removes all active content (scripts, event handlers, forms) and rewrites
// links to open in a new tab with noopener/noreferrer for security.
// Mailto links are marked for in-app handling via postMessage.

import DOMPurify from 'dompurify';

// Bind DOMPurify to the browser (or the jsdom window used by tests). Importing
// jsdom here would pull Node-only code into the client bundle.
const purify = DOMPurify();

// Allowed tags for email content - conservative set for safe rendering
const ALLOWED_TAGS = [
  'a', 'abbr', 'acronym', 'address', 'b', 'bdi', 'bdo', 'big', 'blockquote',
  'br', 'caption', 'center', 'cite', 'code', 'col', 'colgroup', 'dd', 'del',
  'dfn', 'dir', 'div', 'dl', 'dt', 'em', 'font', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
  'hr', 'i', 'img', 'ins', 'kbd', 'li', 'mark', 'ol', 'p', 'pre', 'q', 'rp', 'rt',
  'ruby', 's', 'samp', 'small', 'span', 'strike', 'strong', 'sub', 'sup', 'table',
  'tbody', 'td', 'tfoot', 'th', 'thead', 'tr', 'tt', 'u', 'ul', 'wbr'
];

// Allowed attributes for email content
const ALLOWED_ATTR = [
  'href', 'src', 'alt', 'title', 'width', 'height', 'align', 'valign',
  'colspan', 'rowspan', 'border', 'cellpadding', 'cellspacing', 'style',
  'class', 'id', 'name', 'target', 'rel', 'cite', 'datetime', 'summary', 'data-emissary-mailto'
];

// Allowed CSS properties in style attributes
const ALLOWED_CSS_PROPERTIES = [
  'color', 'background-color', 'font', 'font-family', 'font-size', 'font-weight',
  'font-style', 'text-decoration', 'text-align', 'vertical-align', 'margin',
  'margin-top', 'margin-right', 'margin-bottom', 'margin-left', 'padding',
  'padding-top', 'padding-right', 'padding-bottom', 'padding-left', 'border',
  'border-top', 'border-right', 'border-bottom', 'border-left', 'border-color',
  'border-style', 'border-width', 'width', 'height', 'max-width', 'max-height',
  'display', 'float', 'clear', 'overflow', 'white-space', 'word-wrap',
  'line-height', 'letter-spacing', 'text-transform', 'text-indent', 'direction'
];

// Allow http, https, mailto, and relative URLs in href/src attributes
const ALLOWED_URI_REGEXP = /^(?:(?:https?|mailto):|[^a-z]|$)/i;

// DOMPurify config for email HTML
const PURIFY_CONFIG = {
  ALLOWED_TAGS,
  ALLOWED_ATTR,
  ALLOWED_CSS_PROPERTIES,
  ALLOWED_URI_REGEXP,
  FORBID_TAGS: ['script', 'style', 'iframe', 'frame', 'frameset', 'object', 'embed', 'applet', 'form', 'input', 'textarea', 'select', 'button', 'link', 'meta', 'base'],
  FORBID_ATTR: ['onerror', 'onload', 'onclick', 'onmouseover', 'onmouseout', 'onkeydown', 'onkeyup', 'onkeypress', 'onfocus', 'onblur', 'onchange', 'onsubmit', 'onreset', 'onselect', 'onabort', 'oncanplay', 'oncanplaythrough', 'ondurationchange', 'onemptied', 'onended', 'onerror', 'onloadeddata', 'onloadedmetadata', 'onloadstart', 'onpause', 'onplay', 'onplaying', 'onprogress', 'onratechange', 'onseeked', 'onseeking', 'onstalled', 'onsuspend', 'ontimeupdate', 'onvolumechange', 'onwaiting', 'onwheel', 'ondrag', 'ondragend', 'ondragenter', 'ondragleave', 'ondragover', 'ondragstart', 'ondrop', 'onscroll', 'oncopy', 'oncut', 'onpaste', 'onbeforeunload', 'onhashchange', 'onmessage', 'onoffline', 'ononline', 'onpagehide', 'onpageshow', 'onpopstate', 'onresize', 'onstorage', 'onunload', 'action', 'formaction', 'formmethod', 'formenctype', 'formtarget', 'formnovalidate', 'method', 'enctype'],
  KEEP_CONTENT: true,
  RETURN_DOM: false,
  RETURN_DOM_FRAGMENT: false,
  RETURN_TRUSTED_TYPE: false,
  SANITIZE_DOM: true,
};

/**
 * Sanitizes email HTML for safe rendering.
 * - Removes all scripts, event handlers, forms, and active content
 * - Rewrites all links to open in a new tab with noopener/noreferrer
 * - Preserves safe formatting and images
 */
export function sanitizeEmailHtml(html: string): string {
  if (!html || !html.trim()) return '';

  // First pass: DOMPurify to remove dangerous content
  let sanitized = purify.sanitize(html, PURIFY_CONFIG);

  // Second pass: parse with the browser's DOMParser to rewrite links
  const document = new DOMParser().parseFromString(sanitized, 'text/html');

  // Rewrite all <a> tags to open in new tab
  const anchors = document.querySelectorAll('a[href]');
  for (let i = 0; i < anchors.length; i++) {
    const anchor = anchors[i] as HTMLAnchorElement;
    const href = anchor.getAttribute('href');
    if (!href) continue;

    // Skip javascript: and data: URLs entirely
    if (href.startsWith('javascript:') || href.startsWith('data:') || href.startsWith('vbscript:')) {
      anchor.removeAttribute('href');
      anchor.style.pointerEvents = 'none';
      anchor.style.color = 'inherit';
      anchor.style.textDecoration = 'none';
      anchor.style.cursor = 'default';
      continue;
    }

    // Mailto links: mark for in-app handling (no target=_blank)
    if (href.startsWith('mailto:')) {
      anchor.setAttribute('data-emissary-mailto', href);
      anchor.removeAttribute('target');
      continue;
    }

    // Only rewrite http/https links to open in new tab
    if (href.startsWith('http:') || href.startsWith('https:')) {
      anchor.setAttribute('target', '_blank');
      anchor.setAttribute('rel', 'noopener noreferrer');
    }
  }

  // Remove any remaining event handler attributes that might have slipped through
  const elements = document.querySelectorAll('*');
  for (let i = 0; i < elements.length; i++) {
    const el = elements[i];
    const attrs = Array.from(el.attributes);
    attrs.forEach((attr: Attr) => {
      if (attr.name.startsWith('on') && attr.name.toLowerCase() !== 'onload') {
        el.removeAttribute(attr.name);
      }
    });
  }

  return document.body.innerHTML;
}

// Message type sent from the sandboxed iframe to the parent app when a
// mailto link is clicked. Keep in sync with EmailView.tsx.
export const MAILTO_MESSAGE_TYPE = 'emissary:mailto';

// Trusted script injected into the iframe document. It only handles clicks on
// links we marked with data-emissary-mailto during sanitization. Since all
// user-provided scripts/event handlers were stripped by DOMPurify, this is the
// only script that runs in the frame.
const MAILTO_HANDLER_SCRIPT = `<script>
(function () {
  document.addEventListener('click', function (event) {
    var target = event.target;
    while (target && target.tagName !== 'A') {
      target = target.parentElement;
    }
    if (!target) return;
    var mailto = target.getAttribute && target.getAttribute('data-emissary-mailto');
    if (!mailto) return;
    event.preventDefault();
    if (window.parent && window.parent.postMessage) {
      window.parent.postMessage({ type: '${MAILTO_MESSAGE_TYPE}', href: mailto }, '*');
    }
  }, true);
})();
</script>`;

/**
 * Creates a complete HTML document for iframe rendering with sandbox-safe defaults.
 */
export function createEmailHtmlDocument(html: string): string {
  const sanitized = sanitizeEmailHtml(html);
  return `<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"><style>body{margin:0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;font-size:14px;line-height:1.6;color:#1e293b;word-wrap:break-word}img{max-width:100%;height:auto}a{color:#2563eb}pre{white-space:pre-wrap;word-wrap:break-word;background:#f8fafc;padding:12px;border-radius:4px;border:1px solid #e2e8f0;font-family:ui-monospace,SFMono-Regular,'SF Mono',Menlo,monospace;font-size:13px;line-height:1.5}blockquote{margin:0 0 16px 0;padding-left:16px;border-left:3px solid #e2e8f0;color:#64748b}table{border-collapse:collapse;width:100%}td,th{border:1px solid #e2e8f0;padding:8px}th{background:#f8fafc;font-weight:600}</style></head><body>${sanitized}${MAILTO_HANDLER_SCRIPT}</body></html>`;
}