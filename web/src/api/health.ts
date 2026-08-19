export type HealthState = {
  status: 'ok'
}

export async function fetchHealth(signal?: AbortSignal): Promise<HealthState> {
  const response = await fetch('/healthz', {
    cache: 'no-store',
    headers: { Accept: 'application/json' },
    signal,
  })

  if (!response.ok) {
    throw new Error(`health request failed with status ${response.status}`)
  }

  const body: unknown = await response.json()
  if (!isHealthState(body)) {
    throw new Error('health response has an unexpected shape')
  }
  return body
}

function isHealthState(value: unknown): value is HealthState {
  return typeof value === 'object' && value !== null && 'status' in value && value.status === 'ok'
}
