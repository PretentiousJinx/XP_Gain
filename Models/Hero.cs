namespace MyFitnessRPG.Models
{
    public class Hero
    {
        public string Name { get; set; }
        public string Class { get; set; }   // Warrior, Mage, etc.
        public int Level { get; set; } = 1;

        // Basic stats that players can level up through fitness activity
        public int Strength { get; set; } = 5;
        public int Agility { get; set; } = 5;
        public int Endurance { get; set; } = 5;

        public int Experience { get; set; } = 0;
        public int ExperienceToNextLevel => Level * 100;

        public void AddExperience(int amount)
        {
            Experience += amount;
            while (Experience >= ExperienceToNextLevel)
            {
                Experience -= ExperienceToNextLevel;
                LevelUp();
            }
        }

        private void LevelUp()
        {
            Level++;
            Strength++;
            Agility++;
            Endurance++;
        }

        public override string ToString()
        {
            return $"{Name} the {Class} (Level {Level}) - STR: {Strength}, AGI: {Agility}, END: {Endurance}";
        }
    }
}
