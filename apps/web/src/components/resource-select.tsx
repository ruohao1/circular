import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";

type ResourceSelectProps = {
  id: string;
  label: string;
  value: string;
  onValueChange: (value: string) => void;
  options: { value: string; label: string; disabled?: boolean }[];
  placeholder: string;
  disabled?: boolean;
  required?: boolean;
  className?: string;
};

export function ResourceSelect({
  id,
  label,
  value,
  onValueChange,
  options,
  placeholder,
  disabled = false,
  required = false,
  className,
}: ResourceSelectProps) {
  return (
    <div className={cn("grid min-w-0 gap-2", className)}>
      <Label htmlFor={id}>{label}</Label>
      <Select
        name={id}
        value={value}
        onValueChange={onValueChange}
        disabled={disabled || options.length === 0}
        required={required}
      >
        <SelectTrigger id={id} aria-label={label} className="w-full min-w-0">
          <SelectValue placeholder={placeholder} />
        </SelectTrigger>
        <SelectContent
          position="popper"
          align="start"
          className="w-(--radix-select-trigger-width) max-w-[calc(100vw-2rem)]"
        >
          <SelectGroup>
            {options.map((option) => (
              <SelectItem
                key={option.value}
                value={option.value}
                textValue={option.label}
                disabled={option.disabled}
              >
                <span className="truncate">{option.label}</span>
              </SelectItem>
            ))}
          </SelectGroup>
        </SelectContent>
      </Select>
    </div>
  );
}
