// Renders text with inline LaTeX ($...$ and $$...$$) through KaTeX — the
// L1 paper pipeline returns question stems, answers and analyses with
// math in $-delimited form (Pix2Text convention). Anything KaTeX cannot
// parse falls back to the raw text: a broken formula must never blank
// the card it lives in.
import React, { useMemo } from 'react';
import katex from 'katex';
import 'katex/dist/katex.min.css';

/** One parsed run of the input: plain text or math. */
interface Segment {
  text: string;
  math: boolean;
  /** displayMode for KaTeX ($$...$$). */
  display: boolean;
}

/**
 * Splits "$$block$$ and $inline$ math out of plain text. Lone dollar
 * signs (prices, currencies) stay text: an opening $ only counts when a
 * closing one follows with at least one character between.
 */
export function parseSegments(input: string): Segment[] {
  const out: Segment[] = [];
  let buf = '';
  let i = 0;
  const flush = () => {
    if (buf) {
      out.push({ text: buf, math: false, display: false });
      buf = '';
    }
  };
  while (i < input.length) {
    if (input.startsWith('$$', i)) {
      const end = input.indexOf('$$', i + 2);
      if (end !== -1) {
        flush();
        out.push({ text: input.slice(i + 2, end), math: true, display: true });
        i = end + 2;
        continue;
      }
    }
    if (input[i] === '$') {
      const end = input.indexOf('$', i + 1);
      if (end > i + 1) {
        flush();
        out.push({ text: input.slice(i + 1, end), math: true, display: false });
        i = end + 1;
        continue;
      }
    }
    buf += input[i];
    i++;
  }
  flush();
  return out;
}

const escapeHtml = (s: string) =>
  s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

/** KaTeX with a hard floor: never throws, never returns empty on error. */
function renderMath(tex: string, displayMode: boolean): string {
  try {
    return katex.renderToString(tex, { throwOnError: false, displayMode });
  } catch {
    // Corrupt input (or a KaTeX internal error): show the source, the
    // parent/kid can still read the question.
    return `<code>${escapeHtml(tex)}</code>`;
  }
}

interface MathTextProps {
  /** The text to render; may contain $...$ / $$...$$ LaTeX runs. */
  text?: string;
  className?: string;
  /** Render plain-text runs in this element too (default: the wrapper span). */
  as?: 'span' | 'div' | 'p';
}

/**
 * Text with inline math. Keeps the surrounding layout intact: one wrapper
 * element, math runs rendered inside it.
 */
export const MathText: React.FC<MathTextProps> = ({ text, className, as = 'span' }) => {
  const segments = useMemo(() => (text ? parseSegments(text) : []), [text]);
  if (!text) return null;
  const Tag = as;
  return (
    <Tag className={className}>
      {segments.map((seg, i) =>
        seg.math ? (
          <span
            key={i}
            data-test={seg.display ? 'math-block' : 'math-inline'}
            dangerouslySetInnerHTML={{ __html: renderMath(seg.text, seg.display) }}
          />
        ) : (
          <React.Fragment key={i}>{seg.text}</React.Fragment>
        ),
      )}
    </Tag>
  );
};
