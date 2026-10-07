/** Present persisted provider errors without losing their diagnostic text. */
export function formatChatError(raw: string): { message: string; resetAt?: number } {
  const fallback = { message: raw || 'The request failed. Please try again.' };
  const start = raw.indexOf('{');
  const end = raw.lastIndexOf('}');
  if (start < 0 || end < start) return fallback;
  try {
    const payload = JSON.parse(raw.slice(start, end + 1));
    const error = payload?.error;
    if (!error || typeof error !== 'object') return fallback;
    if (error.type === 'usage_limit_reached') {
      const chatGPT = raw.includes('ChatGPT sign-in');
      const resetAt = typeof error.resets_at === 'number' && Number.isFinite(error.resets_at) && error.resets_at > 0 && error.resets_at * 1000 <= 8.64e15
        ? error.resets_at * 1000 : undefined;
      return {
        message: `${chatGPT ? 'ChatGPT' : 'Provider'} usage limit reached.${raw.includes('(no other model was available)') ? ' No other model was available.' : ''}`,
        resetAt,
      };
    }
    return typeof error.message === 'string' && error.message.trim() ? { message: error.message } : fallback;
  } catch {
    return fallback;
  }
}
