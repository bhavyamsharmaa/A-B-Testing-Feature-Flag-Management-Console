// An invite link opened while signed out has to survive the sign-in (or
// sign-up and email confirmation) detour. sessionStorage is enough: it lasts
// for the tab, and a token is useless to anyone but the invited email anyway.

const KEY = 'helios.pendingInvite'

export function savePendingInvite(token: string): void {
  try {
    sessionStorage.setItem(KEY, token)
  } catch {
    /* private mode: the user just opens the link again */
  }
}

export function takePendingInvite(): string | null {
  try {
    return sessionStorage.getItem(KEY)
  } catch {
    return null
  }
}

export function clearPendingInvite(): void {
  try {
    sessionStorage.removeItem(KEY)
  } catch {
    /* ignore */
  }
}
