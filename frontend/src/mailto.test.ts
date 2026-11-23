import { describe, expect, it } from 'vitest';
import { parseMailtoUrl, mailtoToComposeQuery } from './mailto.ts';

describe('parseMailtoUrl', () => {
  it('parses a simple mailto link with just a recipient', () => {
    const result = parseMailtoUrl('mailto:user@example.com');
    expect(result).toEqual({
      to: 'user@example.com',
      cc: '',
      bcc: '',
      subject: '',
      body: '',
    });
  });

  it('parses multiple recipients', () => {
    const result = parseMailtoUrl('mailto:user1@example.com,user2@example.com');
    expect(result?.to).toBe('user1@example.com, user2@example.com');
  });

  it('parses subject and body', () => {
    const result = parseMailtoUrl('mailto:user@example.com?subject=Hello%20World&body=Hi%20there');
    expect(result?.subject).toBe('Hello World');
    expect(result?.body).toBe('Hi there');
  });

  it('parses cc and bcc', () => {
    const result = parseMailtoUrl('mailto:user@example.com?cc=cc1@example.com,cc2@example.com&bcc=hidden@example.com');
    expect(result?.cc).toBe('cc1@example.com, cc2@example.com');
    expect(result?.bcc).toBe('hidden@example.com');
  });

  it('handles URL-encoded characters', () => {
    const result = parseMailtoUrl('mailto:user@example.com?subject=Hello%20%26%20Welcome&body=Line%201%0ALine%202');
    expect(result?.subject).toBe('Hello & Welcome');
    expect(result?.body).toBe('Line 1\nLine 2');
  });

  it('handles plus signs as spaces', () => {
    const result = parseMailtoUrl('mailto:user@example.com?subject=Hello+World');
    expect(result?.subject).toBe('Hello World');
  });

  it('handles recipients in query parameter', () => {
    const result = parseMailtoUrl('mailto:?to=user@example.com&subject=Test');
    expect(result?.to).toBe('user@example.com');
    expect(result?.subject).toBe('Test');
  });

  it('returns null for non-mailto URLs', () => {
    expect(parseMailtoUrl('https://example.com')).toBeNull();
    expect(parseMailtoUrl('tel:+1234567890')).toBeNull();
  });

  it('handles empty mailto', () => {
    const result = parseMailtoUrl('mailto:');
    expect(result).toEqual({
      to: '',
      cc: '',
      bcc: '',
      subject: '',
      body: '',
    });
  });

  it('handles malformed percent encoding gracefully', () => {
    const result = parseMailtoUrl('mailto:user@example.com?subject=100%done');
    expect(result?.subject).toBe('100%done');
  });
});

describe('mailtoToComposeQuery', () => {
  it('builds query string from mailto data', () => {
    const result = mailtoToComposeQuery({
      to: 'user@example.com',
      cc: 'cc@example.com',
      bcc: '',
      subject: 'Hello World',
      body: 'Hi there',
    });
    expect(result).toContain('to=user%40example.com');
    expect(result).toContain('cc=cc%40example.com');
    expect(result).toContain('subject=Hello+World');
    expect(result).toContain('body=Hi+there');
    expect(result).not.toContain('bcc=');
  });

  it('returns empty string for empty data', () => {
    const result = mailtoToComposeQuery({
      to: '',
      cc: '',
      bcc: '',
      subject: '',
      body: '',
    });
    expect(result).toBe('');
  });
});
