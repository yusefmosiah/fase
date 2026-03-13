import { execFile } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import { defineConfig } from 'vite'

const execFileAsync = promisify(execFile)
const webRoot = fileURLToPath(new URL('.', import.meta.url))
const repoRoot = path.resolve(webRoot, '..')

function cagentExecutable() {
  const binaryPath = path.join(repoRoot, 'bin', 'cagent')
  if (globalThis.process.env.CAGENT_WEB_API_PREFER_BIN === '1' && fs.existsSync(binaryPath)) {
    return { command: binaryPath, args: [] }
  }
  return { command: 'go', args: ['run', './cmd/cagent'] }
}

async function runCagent(args) {
  const executable = cagentExecutable()
  const { stdout } = await execFileAsync(
    executable.command,
    [...executable.args, '--json', ...args],
    {
      cwd: repoRoot,
      env: globalThis.process.env,
      maxBuffer: 16 * 1024 * 1024,
    },
  )
  return stdout.trim() === '' ? null : JSON.parse(stdout)
}

async function readJSONBody(req) {
  const chunks = []
  for await (const chunk of req) {
    chunks.push(chunk)
  }
  if (chunks.length === 0) {
    return {}
  }
  return JSON.parse(globalThis.Buffer.concat(chunks).toString('utf8'))
}

function writeJSON(res, statusCode, payload) {
  res.statusCode = statusCode
  res.setHeader('Content-Type', 'application/json')
  res.end(JSON.stringify(payload))
}

function cagentApiPlugin() {
  const handler = async (req, res, next) => {
    if (!req.url?.startsWith('/api/')) {
      next()
      return
    }

    const url = new URL(req.url, 'http://localhost')
    const parts = url.pathname.split('/').filter(Boolean)

    try {
      if (req.method === 'GET' && url.pathname === '/api/work/items') {
        const payload = await runCagent(['work', 'list', '--limit', '500'])
        writeJSON(res, 200, payload)
        return
      }

      if (parts[0] === 'api' && parts[1] === 'work' && parts[2]) {
        const workId = parts[2]

        if (req.method === 'GET' && parts.length === 3) {
          const payload = await runCagent(['work', 'show', workId])
          writeJSON(res, 200, payload)
          return
        }

        if (req.method === 'GET' && parts[3] === 'hydrate') {
          const mode = url.searchParams.get('mode') ?? 'standard'
          const payload = await runCagent(['work', 'hydrate', workId, '--mode', mode])
          writeJSON(res, 200, payload)
          return
        }

        if (req.method === 'POST' && parts[3]) {
          const body = await readJSONBody(req)

          switch (parts[3]) {
            case 'lock': {
              const payload = await runCagent(['work', 'lock', workId])
              writeJSON(res, 200, payload)
              return
            }
            case 'unlock': {
              const payload = await runCagent(['work', 'unlock', workId])
              writeJSON(res, 200, payload)
              return
            }
            case 'approve': {
              const args = ['work', 'approve', workId]
              if (body.message) {
                args.push('--message', body.message)
              }
              const payload = await runCagent(args)
              writeJSON(res, 200, payload)
              return
            }
            case 'reject': {
              const args = ['work', 'reject', workId]
              if (body.message) {
                args.push('--message', body.message)
              }
              const payload = await runCagent(args)
              writeJSON(res, 200, payload)
              return
            }
            case 'promote': {
              const args = ['work', 'promote', workId, '--environment', body.environment || 'staging']
              if (body.ref) {
                args.push('--ref', body.ref)
              }
              if (body.message) {
                args.push('--message', body.message)
              }
              const payload = await runCagent(args)
              writeJSON(res, 200, payload)
              return
            }
            case 'attest': {
              const args = ['work', 'attest', workId, '--result', body.result || 'passed']
              if (body.summary) {
                args.push('--summary', body.summary)
              }
              if (body.verifierKind) {
                args.push('--verifier-kind', body.verifierKind)
              }
              if (body.method) {
                args.push('--method', body.method)
              }
              const payload = await runCagent(args)
              writeJSON(res, 200, payload)
              return
            }
            default:
              writeJSON(res, 404, { error: `unknown action: ${parts[3]}` })
              return
          }
        }
      }

      writeJSON(res, 404, { error: 'not found' })
    } catch (error) {
      writeJSON(res, 500, {
        error: error instanceof Error ? error.message : 'unexpected error',
      })
    }
  }

  return {
    name: 'cagent-local-api',
    configureServer(server) {
      server.middlewares.use(handler)
    },
    configurePreviewServer(server) {
      server.middlewares.use(handler)
    },
  }
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [cagentApiPlugin()],
})
