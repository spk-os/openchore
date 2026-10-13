// @vitest-environment jsdom
// MathText (L1): the LaTeX renderer behind every question/answer field.
// $$…$$ renders display math, $…$ inline math, a lone $ stays literal
// text, and KaTeX failures degrade to the raw source instead of crashing.
import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/react';
import { MathText, parseSegments } from './MathText';

describe('parseSegments', () => {
  it('splits plain text into one text segment', () => {
    expect(parseSegments('no math here')).toEqual([
      { text: 'no math here', math: false, display: false },
    ]);
  });

  it('extracts inline math between dollars', () => {
    expect(parseSegments('solve $x+2=5$ now')).toEqual([
      { text: 'solve ', math: false, display: false },
      { text: 'x+2=5', math: true, display: false },
      { text: ' now', math: false, display: false },
    ]);
  });

  it('extracts display math between double dollars', () => {
    expect(parseSegments('a $$\\frac12$$ b')).toEqual([
      { text: 'a ', math: false, display: false },
      { text: '\\frac12', math: true, display: true },
      { text: ' b', math: false, display: false },
    ]);
  });

  it('keeps two lone dollars as text unless a real pair spans them', () => {
    // "5$ and $6" is a $-pair with content between, so it parses as math:
    // the parser cannot know it was currency. This documents the contract —
    // single unpaired dollars (no closing $ anywhere) stay literal.
    expect(parseSegments('price: 5$ and $6')).toEqual([
      { text: 'price: 5', math: false, display: false },
      { text: ' and ', math: true, display: false },
      { text: '6', math: false, display: false },
    ]);
    // A single $ with no partner at all stays plain text.
    expect(parseSegments('costs 5$ total')).toEqual([
      { text: 'costs 5$ total', math: false, display: false },
    ]);
  });
});

describe('MathText', () => {
  it('renders inline math as KaTeX markup', () => {
    const { container } = render(<MathText text="solve $x+2=5$" />);
    expect(container.querySelector('.katex')).toBeTruthy();
    expect(container.querySelector('[data-test="math-inline"]')).toBeTruthy();
    // The surrounding plain text is kept.
    expect(container.textContent).toContain('solve');
  });

  it('renders display math in a katex-display block', () => {
    const { container } = render(<MathText text="$$\\frac12$$" />);
    expect(container.querySelector('.katex-display')).toBeTruthy();
    expect(container.querySelector('[data-test="math-block"]')).toBeTruthy();
  });

  it('leaves plain text untouched (no katex span)', () => {
    const { container } = render(<MathText text="just words" />);
    expect(container.querySelector('.katex')).toBeNull();
    expect(container.textContent).toBe('just words');
  });

  it('renders nothing on undefined input', () => {
    const { container } = render(<MathText text={undefined} />);
    expect(container.textContent).toBe('');
  });

  it('degrades an invalid expression instead of throwing', () => {
    // throwOnError:false lets KaTeX color the error (class katex-error);
    // the component must never crash the screen over a bad AI formula.
    const { container } = render(<MathText text="$\\unknowncmd{$" />);
    expect(container.querySelector('[class*="katex"]')).toBeTruthy();
    expect(container.textContent).toContain('\\unknowncmd{');
  });
});
