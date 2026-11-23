import { describe, expect, it } from 'vitest';
import { sanitizeEmailHtml, createEmailHtmlDocument } from './sanitize.ts';

describe('sanitizeEmailHtml', () => {
  it('removes script tags', () => {
    const html = '<p>Hello</p><script>alert("xss")</script><p>World</p>';
    const result = sanitizeEmailHtml(html);
    expect(result).not.toContain('<script>');
    expect(result).not.toContain('alert');
    expect(result).toContain('Hello');
    expect(result).toContain('World');
  });

  it('removes event handlers', () => {
    const html = '<a href="https://example.com" onclick="steal()">Click</a>';
    const result = sanitizeEmailHtml(html);
    expect(result).not.toContain('onclick');
    expect(result).toContain('href="https://example.com"');
  });

  it('removes javascript: URLs', () => {
    const html = '<a href="javascript:alert(1)">Bad</a>';
    const result = sanitizeEmailHtml(html);
    expect(result).not.toContain('javascript:');
    // DOMPurify strips href entirely for javascript: URLs, making link non-functional
    expect(result).toBe('<a>Bad</a>');
  });

  it('removes data: URLs', () => {
    const html = '<a href="data:text/html,<script>alert(1)</script>">Bad</a>';
    const result = sanitizeEmailHtml(html);
    expect(result).not.toContain('data:');
  });

  it('rewrites http/https links to open in new tab', () => {
    const html = '<a href="https://example.com">Link</a><a href="http://test.com">Link2</a>';
    const result = sanitizeEmailHtml(html);
    expect(result).toContain('target="_blank"');
    expect(result).toContain('rel="noopener noreferrer"');
  });

  it('marks mailto links for in-app handling (no new tab)', () => {
    const html = '<a href="mailto:test@example.com">Email</a>';
    const result = sanitizeEmailHtml(html);
    expect(result).toContain('data-emissary-mailto="mailto:test@example.com"');
    expect(result).not.toContain('target="_blank"');
  });

  it('preserves safe formatting tags', () => {
    const html = '<p><strong>Bold</strong> and <em>italic</em></p><ul><li>Item</li></ul>';
    const result = sanitizeEmailHtml(html);
    expect(result).toContain('<strong>Bold</strong>');
    expect(result).toContain('<em>italic</em>');
    expect(result).toContain('<ul>');
    expect(result).toContain('<li>Item</li>');
  });

  it('preserves images with safe attributes', () => {
    const html = '<img src="https://example.com/image.png" alt="Test" width="100" height="50">';
    const result = sanitizeEmailHtml(html);
    expect(result).toContain('src="https://example.com/image.png"');
    expect(result).toContain('alt="Test"');
    expect(result).toContain('width="100"');
    expect(result).toContain('height="50"');
  });

  it('removes forms and inputs', () => {
    const html = '<form action="/submit"><input type="text" name="data"><button>Submit</button></form>';
    const result = sanitizeEmailHtml(html);
    expect(result).not.toContain('<form');
    expect(result).not.toContain('<input');
    expect(result).not.toContain('<button');
  });

  it('removes iframes and embeds', () => {
    const html = '<iframe src="https://evil.com"></iframe><embed src="evil.swf">';
    const result = sanitizeEmailHtml(html);
    expect(result).not.toContain('<iframe');
    expect(result).not.toContain('<embed');
  });

  it('removes style tags but preserves style attributes', () => {
    const html = '<style>body { background: red; }</style><p style="color: blue;">Text</p>';
    const result = sanitizeEmailHtml(html);
    expect(result).not.toContain('<style>');
    expect(result).toContain('style="color: blue;"');
  });

  it('handles empty or whitespace input', () => {
    expect(sanitizeEmailHtml('')).toBe('');
    expect(sanitizeEmailHtml('   ')).toBe('');
    expect(sanitizeEmailHtml('<p></p>')).toBe('<p></p>');
  });

  it('preserves tables', () => {
    const html = '<table><thead><tr><th>Header</th></tr></thead><tbody><tr><td>Cell</td></tr></tbody></table>';
    const result = sanitizeEmailHtml(html);
    expect(result).toContain('<table>');
    expect(result).toContain('<thead>');
    expect(result).toContain('<th>Header</th>');
    expect(result).toContain('<td>Cell</td>');
  });

  it('removes base tag', () => {
    const html = '<base href="https://evil.com/"><a href="page.html">Link</a>';
    const result = sanitizeEmailHtml(html);
    expect(result).not.toContain('<base');
  });

  it('removes meta refresh', () => {
    const html = '<meta http-equiv="refresh" content="0;url=https://evil.com">';
    const result = sanitizeEmailHtml(html);
    expect(result).not.toContain('<meta');
  });
});

describe('createEmailHtmlDocument', () => {
  it('wraps sanitized HTML in a complete document', () => {
    const html = '<p>Hello <a href="https://example.com">World</a></p>';
    const result = createEmailHtmlDocument(html);
    expect(result).toContain('<!doctype html>');
    expect(result).toContain('<html>');
    expect(result).toContain('<head>');
    expect(result).toContain('<body>');
    expect(result).toContain('target="_blank"');
    expect(result).toContain('rel="noopener noreferrer"');
  });

  it('includes mailto click handler script', () => {
    const result = createEmailHtmlDocument('<a href="mailto:test@example.com">Email</a>');
    expect(result).toContain('<script>');
    expect(result).toContain('emissary:mailto');
    expect(result).toContain('postMessage');
  });

  it('includes viewport meta tag', () => {
    const result = createEmailHtmlDocument('<p>Test</p>');
    expect(result).toContain('name="viewport"');
  });

  it('includes base styles for email rendering', () => {
    const result = createEmailHtmlDocument('<p>Test</p>');
    expect(result).toContain('font-family');
    expect(result).toContain('word-wrap:break-word');
    expect(result).toContain('max-width:100%');
  });
});