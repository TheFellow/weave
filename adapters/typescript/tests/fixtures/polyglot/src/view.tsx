import { createGreeter } from './service.js'

export function Welcome({ name }: { name: string }) {
  const greeter = createGreeter()
  return <strong>{greeter.greet(name)}</strong>
}
