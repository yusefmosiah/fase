import { execFile } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import { defineConfig } from 'vite'

const execFileAsync = promisify(execFile)
const webRoot = fileURLToPath(new URL('.', import.meta.url))
const repoRoot = path.resolve(webRoot, '..')

// CAGENT_TARGET_REPO sets which repo's work graph to display.
// Defaults to the cagent repo itself if not set.
const targetRepo = globalThis.process.env.CAGENT_TARGET_REPO || repoRoot

function cagentExecutable() {
  // Prefer bin/cagent (freshly built), then go run
  const binaryPath = path.join(repoRoot, 'bin', 'cagent')
  if (fs.existsSync(binaryPath)) {
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
      cwd: targetRepo,
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
      // Supervisor state + live diff
      if (req.method === 'GET' && url.pathname === '/api/supervisor/status') {
        try {
          const supPath = path.join(targetRepo, '.cagent', 'supervisor.json')
          const supData = fs.existsSync(supPath) ? JSON.parse(fs.readFileSync(supPath, 'utf-8')) : null
          const { stdout: diff } = await execFileAsync('git', ['diff', '--stat'], {
            cwd: targetRepo, maxBuffer: 1024 * 1024,
          }).catch(() => ({ stdout: '' }))
          writeJSON(res, 200, { supervisor: supData, diff_stat: diff })
        } catch (e) {
          writeJSON(res, 200, { supervisor: null, diff_stat: '' })
        }
        return
      }

      if (req.method === 'GET' && url.pathname === '/api/diff') {
        try {
          const { stdout } = await execFileAsync('git', ['diff', '--patch'], {
            cwd: targetRepo, maxBuffer: 4 * 1024 * 1024,
          })
          writeJSON(res, 200, { diff: stdout })
        } catch (e) {
          writeJSON(res, 200, { diff: '' })
        }
        return
      }

      // Bash command log from a job's raw session data
      if (req.method === 'GET' && url.pathname === '/api/bash-log') {
        const jobId = url.searchParams.get('job') || 'latest'
        try {
          const rawDir = path.join(targetRepo, '.cagent', 'raw', 'stdout')
          let jobDir
          if (jobId === 'latest') {
            // Find the most recent job dir
            const dirs = fs.readdirSync(rawDir).filter(d => d.startsWith('job_')).sort().reverse()
            jobDir = dirs[0] ? path.join(rawDir, dirs[0]) : null
          } else {
            jobDir = path.join(rawDir, jobId)
          }

          if (!jobDir || !fs.existsSync(jobDir)) {
            writeJSON(res, 200, { commands: [], job_id: jobId })
            return
          }

          const files = fs.readdirSync(jobDir).filter(f => f.endsWith('.jsonl')).sort()
          const commands = []
          for (const f of files) {
            const content = fs.readFileSync(path.join(jobDir, f), 'utf-8')
            for (const line of content.split('\n')) {
              if (!line.trim()) continue
              try {
                const ev = JSON.parse(line)
                if (ev.type === 'item.completed' && ev.item?.type === 'command_execution') {
                  let cmd = ev.item.command || ''
                  if (cmd.startsWith('/bin/zsh -lc ')) {
                    cmd = cmd.slice(13)
                    if ((cmd.startsWith("'") && cmd.endsWith("'")) || (cmd.startsWith('"') && cmd.endsWith('"')))
                      cmd = cmd.slice(1, -1)
                  }
                  commands.push({
                    command: cmd,
                    exit_code: ev.item.exit_code,
                    output_preview: (ev.item.aggregated_output || '').slice(0, 500),
                  })
                } else if (ev.type === 'item.completed' && ev.item?.type === 'agent_message') {
                  const text = (ev.item.text || '').slice(0, 300)
                  if (text) commands.push({ comment: text })
                }
              } catch {}
            }
          }
          writeJSON(res, 200, { commands, job_id: jobDir.split('/').pop() })
        } catch (e) {
          writeJSON(res, 200, { commands: [], error: e.message })
        }
        return
      }

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

        if (req.method === 'GET' && parts[3] === 'docs') {
          const payload = await runCagent(['work', 'show', workId])
          writeJSON(res, 200, payload)
          return
        }

        if (req.method === 'GET' && parts[3] === 'diff') {
          // Return git diff for the target repo
          try {
            const { stdout } = await execFileAsync('git', ['diff', '--stat', '--patch'], {
              cwd: targetRepo,
              maxBuffer: 4 * 1024 * 1024,
            })
            writeJSON(res, 200, { diff: stdout, repo: targetRepo })
          } catch (e) {
            writeJSON(res, 200, { diff: '', repo: targetRepo })
          }
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
