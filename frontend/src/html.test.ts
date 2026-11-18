import { describe, expect, it } from 'vitest';
import { htmlToText, isHtmlEmpty } from './html.ts';

describe('htmlToText', () => {
  it('turns blocks and line breaks into newlines', () => {
    expect(htmlToText('<p>Hello</p><p>World<br>again</p>')).toBe('Hello\nWorld\nagain');
  });

  it('keeps text from nested formatting and links', () => {
    expect(htmlToText('<div><b>Hi</b> <a href="https://example.com">there</a></div>')).toBe('Hi there');
  });

  it('detects messages with no visible text', () => {
    expect(isHtmlEmpty('<p><br></p>')).toBe(true);
    expect(isHtmlEmpty('<p>Hello</p>')).toBe(false);
  });

  it('preserves the reply quote layout: six blank lines above and one below the separator', () => {
    const html =
      'New text' +
      '<br>'.repeat(7) +
      '<div class="emissary-quote-separator">-----</div>' +
      '<br>' +
      '<pre class="emissary-quote">Old text</pre>';
    expect(htmlToText(html)).toBe('New text' + '\n'.repeat(7) + '-----' + '\n\n' + 'Old text');
  });
});
