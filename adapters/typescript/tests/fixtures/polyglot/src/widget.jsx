import { legacyGreeting } from './legacy.js'

/** @param {{name: string}} props */
export function LegacyWidget({ name }) {
  return <span>{legacyGreeting(name)}</span>
}
