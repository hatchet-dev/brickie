export function Logo() {
  return (
    <span className="flex items-center gap-2">
      <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 49 49" width="24" height="24" fill="none" aria-hidden="true">
        <path
          fill="var(--brand)"
          d="m25.22 21.618 6.486-14.412 12.402 12.402c1.713 1.713 2.569 2.569 2.89 3.556a4.324 4.324 0 0 1 0 2.672c-.321.987-1.177 1.843-2.89 3.556L31.706 41.794H21.618l16.936-18.239c.568-.612.852-.918.866-1.179a.72.72 0 0 0-.258-.59c-.2-.168-.618-.168-1.453-.168H25.221ZM23.78 27.382l-6.486 14.412L4.892 29.392c-1.713-1.713-2.569-2.569-2.89-3.556a4.324 4.324 0 0 1 0-2.672c.321-.987 1.177-1.843 2.89-3.556L17.294 7.207h10.088L10.447 25.445c-.568.612-.852.918-.866 1.179a.72.72 0 0 0 .258.59c.2.168.618.168 1.453.168H23.78Z"
        />
      </svg>
      <span className="font-semibold">Brickie</span>
      <span className="rounded-full border border-fd-border px-1.5 py-px text-[10px] font-medium uppercase tracking-wide text-fd-muted-foreground">
        beta
      </span>
    </span>
  );
}
