import { useId } from 'react'
import { Checkbox } from '@/components/ui/checkbox'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

interface FieldSchema {
  type?: string
  title?: string
  description?: string
  enum?: string[]
}

interface FormSchema {
  properties?: Record<string, FieldSchema>
  required?: string[]
}

function fieldsOf(schema: unknown): [string, FieldSchema][] {
  const properties = (schema as FormSchema | undefined)?.properties
  return properties ? Object.entries(properties) : []
}

export function missingRequired(schema: unknown, values: Record<string, unknown>): boolean {
  const required = (schema as FormSchema | undefined)?.required ?? []
  return required.some((name) => values[name] === undefined || values[name] === '')
}

function EnumField({ label, help, options, value, onChange }: {
  label: string
  help?: string
  options: string[]
  value: string | undefined
  onChange: (value: string) => void
}) {
  const id = useId()
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger id={id} aria-describedby={help ? `${id}-help` : undefined}>
          <SelectValue placeholder="Choose one" />
        </SelectTrigger>
        <SelectContent>
          {options.map((option) => <SelectItem key={option} value={option}>{option}</SelectItem>)}
        </SelectContent>
      </Select>
      {help && <p id={`${id}-help`} className="text-ui-sm text-muted">{help}</p>}
    </div>
  )
}

export function FormFields({ schema, values, onChange }: {
  schema: unknown
  values: Record<string, unknown>
  onChange: (values: Record<string, unknown>) => void
}) {
  const required = new Set((schema as FormSchema | undefined)?.required ?? [])
  const set = (name: string, value: unknown) => {
    const next = { ...values }
    if (value === undefined) delete next[name]
    else next[name] = value
    onChange(next)
  }
  return (
    <div className="flex flex-col gap-2">
      {fieldsOf(schema).map(([name, field]) => {
        const label = `${field.title ?? name}${required.has(name) ? ' (required)' : ''}`
        if (field.type === 'boolean') {
          return (
            <label key={name} className="flex items-center gap-2 text-ui">
              <Checkbox checked={values[name] === true} onCheckedChange={(checked) => set(name, checked === true)} />
              {label}
            </label>
          )
        }
        if (field.enum) {
          return <EnumField key={name} label={label} help={field.description} options={field.enum} value={values[name] as string | undefined} onChange={(value) => set(name, value)} />
        }
        const numeric = field.type === 'number' || field.type === 'integer'
        return (
          <FormField key={name} label={label} help={field.description}>
            <Input
              type={numeric ? 'number' : 'text'}
              value={String(values[name] ?? '')}
              onChange={(event) => {
                const text = event.target.value
                set(name, numeric ? (text === '' ? undefined : Number(text)) : text)
              }}
            />
          </FormField>
        )
      })}
    </div>
  )
}
