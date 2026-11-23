// Parse a mailto: URL into fields used by the compose view.
// mailto:user@example.com?subject=Hello&cc=cc@example.com&body=Hi

export interface MailtoData {
  to: string;
  cc: string;
  bcc: string;
  subject: string;
  body: string;
}

export function parseMailtoUrl(href: string): MailtoData | null {
  if (!href.startsWith('mailto:')) return null;

  const result: MailtoData = {
    to: '',
    cc: '',
    bcc: '',
    subject: '',
    body: '',
  };

  // Split off the query part (after ?)
  const questionIndex = href.indexOf('?');
  const toPart = questionIndex >= 0 ? href.slice(7, questionIndex) : href.slice(7);
  const queryPart = questionIndex >= 0 ? href.slice(questionIndex + 1) : '';

  // Recipients are comma-separated and URL-encoded
  if (toPart) {
    result.to = toPart.split(',').map((s) => safeDecode(s)).filter(Boolean).join(', ');
  }

  // Parse query parameters
  if (queryPart) {
    // Split on & (but not &amp; which might be in encoded content)
    const params = queryPart.split('&');
    for (const param of params) {
      const eqIndex = param.indexOf('=');
      if (eqIndex < 0) continue;

      const key = safeDecode(param.slice(0, eqIndex)).toLowerCase();
      const value = safeDecode(param.slice(eqIndex + 1));

      switch (key) {
        case 'to':
          result.to = value.split(',').map((s) => s.trim()).filter(Boolean).join(', ');
          break;
        case 'cc':
          result.cc = value.split(',').map((s) => s.trim()).filter(Boolean).join(', ');
          break;
        case 'bcc':
          result.bcc = value.split(',').map((s) => s.trim()).filter(Boolean).join(', ');
          break;
        case 'subject':
          result.subject = value;
          break;
        case 'body':
          result.body = value;
          break;
      }
    }
  }

  return result;
}

function safeDecode(value: string): string {
  try {
    return decodeURIComponent(value.replace(/\+/g, ' '));
  } catch {
    return value.replace(/\+/g, ' ');
  }
}

// Build compose URL query string from mailto data
export function mailtoToComposeQuery(data: MailtoData): string {
  const params = new URLSearchParams();
  if (data.to) params.set('to', data.to);
  if (data.cc) params.set('cc', data.cc);
  if (data.bcc) params.set('bcc', data.bcc);
  if (data.subject) params.set('subject', data.subject);
  if (data.body) params.set('body', data.body);
  const query = params.toString();
  return query ? `?${query}` : '';
}
