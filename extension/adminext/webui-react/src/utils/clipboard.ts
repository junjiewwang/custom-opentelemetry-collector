/**
 * Copy text to the clipboard.
 *
 * The async Clipboard API is only available in secure contexts (HTTPS or
 * localhost). On plain HTTP origins `navigator.clipboard` is undefined, so we
 * fall back to the legacy `execCommand('copy')` path. That path must run
 * synchronously inside the user gesture, hence the non-secure branch executes
 * the fallback immediately instead of after a rejected promise.
 */
export function copyText(text: string): Promise<boolean> {
  if (!text) {
    return Promise.resolve(false);
  }

  if (!window.isSecureContext || !navigator.clipboard?.writeText) {
    return Promise.resolve(legacyCopy(text));
  }

  return navigator.clipboard
    .writeText(text)
    .then(() => true)
    .catch(() => legacyCopy(text));
}

function legacyCopy(text: string): boolean {
  const activeElement = document.activeElement as HTMLElement | null;
  const selection = document.getSelection();
  const savedRange = selection && selection.rangeCount > 0 ? selection.getRangeAt(0).cloneRange() : null;

  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.setAttribute('readonly', '');
  textarea.style.position = 'fixed';
  textarea.style.top = '0';
  textarea.style.left = '0';
  textarea.style.opacity = '0';
  textarea.style.pointerEvents = 'none';
  document.body.appendChild(textarea);

  try {
    textarea.focus();
    textarea.select();
    textarea.setSelectionRange(0, text.length);
    return document.execCommand('copy');
  } catch {
    return false;
  } finally {
    document.body.removeChild(textarea);
    if (selection && savedRange) {
      selection.removeAllRanges();
      selection.addRange(savedRange);
    }
    activeElement?.focus();
  }
}
