// Import every route file once, for its registerRoute side effect. New views
// (run board, terminal, diffs, team surfaces) add one line here.
import '@/routes/board'
import '@/routes/diff'
import '@/routes/overview'
import '@/routes/run'
import '@/routes/session'
import '@/routes/team'
import '@/routes/terminal'
import '@/routes/terminal/events'

export { lookupRoute, registerRoute, type RouteProps } from '@/routes/registry'
