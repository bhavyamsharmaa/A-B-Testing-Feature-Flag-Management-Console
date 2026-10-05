import { ApiError } from '../api/client'

/** Turns any failure into a message a person can act on. `action` reads as "…to <action>". */
export function describeError(err: unknown, action: string): string {
  if (!(err instanceof ApiError)) return 'Something went wrong. Try again.'
  switch (true) {
    case err.status === 0:
      return err.message // NETWORK / TIMEOUT already read well
    case err.status === 403:
      return `You don't have permission to ${action}. ${err.message}`
    case err.status === 404:
      return `Not found: ${err.message}. Refresh the list; it may have changed.`
    case err.status >= 500:
      return `The server hit an error (${err.status}). Try again in a moment.`
    default:
      return err.message
  }
}
