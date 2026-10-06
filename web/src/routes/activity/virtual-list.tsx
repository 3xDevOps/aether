import { createContext, type ReactElement, type RefObject, useContext } from 'react'
import { type CustomItemComponentProps, Virtualizer } from 'virtua'

const SetSize = createContext(0)

function Item({ style, index, children, ref }: CustomItemComponentProps) {
  const size = useContext(SetSize)
  return (
    <li ref={ref} style={style} aria-setsize={size} aria-posinset={index + 1} className="border-b border-seam last:border-b-0">
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
  return (
    <SetSize.Provider value={data.length}>
      <Virtualizer as="ol" item={Item} data={data} scrollRef={scrollRef} bufferSize={400}>
        {children}
      </Virtualizer>
    </SetSize.Provider>
  )
}
