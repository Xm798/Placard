import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { __resetAuthRedirect, api, type HttpError } from './api'

function mockResponse(status: number): Response {
  return { ok: status >= 200 && status < 300, status } as Response
}

const assignMock = vi.fn()

beforeEach(() => {
  __resetAuthRedirect()
  assignMock.mockReset()
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { assign: assignMock, pathname: '/files' },
  })
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('api', () => {
  test('returns the response when ok', async () => {
    const resp = mockResponse(200)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(resp))
    await expect(api('/api/files')).resolves.toBe(resp)
  })

  test('sends X-Requested-With header and same-origin credentials', async () => {
    const fetchMock = vi.fn().mockResolvedValue(mockResponse(200))
    vi.stubGlobal('fetch', fetchMock)
    await api('/api/files')
    const [, init] = fetchMock.mock.calls[0]
    expect(init.headers['X-Requested-With']).toBe('fetch')
    expect(init.credentials).toBe('same-origin')
  })

  test('two concurrent 401s redirect exactly once', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(mockResponse(401)))
    const results = await Promise.allSettled([api('/api/a'), api('/api/b')])
    expect(results.every((r) => r.status === 'rejected')).toBe(true)
    expect(assignMock).toHaveBeenCalledTimes(1)
    expect(assignMock).toHaveBeenCalledWith('/login?redirect=' + encodeURIComponent('/files'))
  })

  test('500 throws with .status === 500', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(mockResponse(500)))
    await expect(api('/api/x')).rejects.toMatchObject({ status: 500 })
    try {
      await api('/api/x')
    } catch (e) {
      expect((e as HttpError).status).toBe(500)
    }
  })
})
