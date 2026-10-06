import { createContext, type ReactElement, type RefObject, useContext, useLayoutEffect, useRef, useState } from 'react'
import { type CustomItemComponentProps, Virtualizer } from 'virtua'

const SetSize = createContext(0)
const FocusRow = createContext<(index: number) => void>(() => {})

function Item({ style, index, children, ref }: CustomItemComponentProps) {
  const size = useContext(SetSize)
  const focusRow = useContext(FocusRow)
  return (
    <li ref={ref} style={style} aria-setsize={size} aria-posinset={index + 1} onFocus={() => focusRow(index)} className="border-b border-seam last:border-b-0">
      {children}
    </li>
  )
}

export function VirtualList<T>({
  data,
  scrollRef,
  children,
}: {
  data: readonly T[]
  scrollRef: RefObject<HTMLElement | null>
  children: (item: T, index: number) => ReactElement
}) {
  const previous = useRef(data)
  const [focused, setFocused] = useState<number | null>(null)
  useLayoutEffect(() => {
    previous.current = data
  })
  const before = previous.current
  const prepended = before.length > 0 && data.length > before.length && data[data.length - before.length] === before[0]
  // virtua keeps the reader's place from the list's end; at the very top the reader should see the new rows.
  const shift = prepended && (scrollRef.current?.scrollTop ?? 0) > 0
  return (
    <SetSize.Provider value={data.length}>
      <FocusRow.Provider value={setFocused}>
        <Virtualizer
          as="ol"
          item={Item}
          data={data}
          scrollRef={scrollRef}
          bufferSize={400}
          shift={shift}
          keepMounted={focused !== null && focused < data.length ? [focused] : undefined}
        >
          {children}
        </Virtualizer>
      </FocusRow.Provider>
    </SetSize.Provider>
  )
}
