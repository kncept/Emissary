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
});
